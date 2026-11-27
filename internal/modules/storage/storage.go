// Package storage scans configured shares to report per-share and per-folder
// disk usage, file-type breakdowns, largest/oldest files, and exact duplicates.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"netshare-hub/internal/sys"
)

const (
	maxTopFolders    = 12
	maxTopFiles      = 50
	maxDuplicateGrps = 30
	minDupBytes      = 1024
	maxDupHashFiles  = 2000
	maxDupFileBytes  = sys.MaxHashFileBytes // the agent hashes no bigger file
)

// ShareRef identifies one share to analyze.
type ShareRef struct {
	ID   int64
	Name string
	Path string
}

// StorageCategoryStat aggregates bytes and file count for one media category.
type StorageCategoryStat struct {
	Category string `json:"category"`
	Bytes    int64  `json:"bytes"`
	Files    int64  `json:"files"`
}

// StorageFolderStat summarizes one top-level folder inside a share.
type StorageFolderStat struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Files int64  `json:"files"`
}

// StorageShareStat holds usage totals and breakdowns for one share.
type StorageShareStat struct {
	ShareID    int64                 `json:"shareId"`
	ShareName  string                `json:"shareName"`
	Path       string                `json:"path"`
	TotalBytes int64                 `json:"totalBytes"`
	FileCount  int64                 `json:"fileCount"`
	DirCount   int64                 `json:"dirCount"`
	TopFolders []StorageFolderStat   `json:"topFolders" nullable:"false"`
	Categories []StorageCategoryStat `json:"categories" nullable:"false"`
}

// StorageFileItem represents a notable file (largest or oldest).
type StorageFileItem struct {
	ShareName string `json:"shareName"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	ModTime   string `json:"modTime"`
}

// StorageDuplicateFile is one copy inside a duplicate group.
type StorageDuplicateFile struct {
	ShareName string `json:"shareName"`
	Path      string `json:"path"`
	ModTime   string `json:"modTime"`
}

// StorageDuplicateGroup holds two or more files with identical SHA-256 content.
type StorageDuplicateGroup struct {
	SHA256           string                 `json:"sha256"`
	Size             int64                  `json:"size"`
	ReclaimableBytes int64                  `json:"reclaimableBytes"`
	Files            []StorageDuplicateFile `json:"files" nullable:"false"`
}

// StorageReport is the full snapshot returned by GET /storage/report.
type StorageReport struct {
	ScannedAt        string                  `json:"scannedAt,omitempty"`
	DurationMs       int64                   `json:"durationMs"`
	Scanning         bool                    `json:"scanning"`
	TotalBytes       int64                   `json:"totalBytes"`
	TotalFiles       int64                   `json:"totalFiles"`
	TotalDirs        int64                   `json:"totalDirs"`
	ReclaimableBytes int64                   `json:"reclaimableBytes"`
	Shares           []StorageShareStat      `json:"shares" nullable:"false"`
	LargestFiles     []StorageFileItem       `json:"largestFiles" nullable:"false"`
	OldestFiles      []StorageFileItem       `json:"oldestFiles" nullable:"false"`
	Duplicates       []StorageDuplicateGroup `json:"duplicates" nullable:"false"`
}

// Service runs storage scans and persists the latest report to disk.
type Service struct {
	reportPath string
	listShares func(ctx context.Context) ([]ShareRef, error)
	files      Files

	scanning atomic.Bool
	mu       sync.RWMutex
	latest   StorageReport
}

// NewService constructs the storage analyzer and loads any saved report.
// Files reads the share folders. It is the system layer (sys.Samba): the
// web server itself cannot read them, only their users and the agent can.
type Files interface {
	ScanShare(ctx context.Context, share string, fn func(sys.ShareEntry) error) error
	StatFiles(ctx context.Context, share string, paths []string, hash bool) ([]sys.ShareFile, error)
}

func NewService(dataDir string, listShares func(ctx context.Context) ([]ShareRef, error), files Files) *Service {
	s := &Service{
		reportPath: filepath.Join(dataDir, "storage-report.json"),
		listShares: listShares,
		files:      files,
		latest:     emptyReport(),
	}
	if raw, err := os.ReadFile(s.reportPath); err == nil {
		var saved StorageReport
		if json.Unmarshal(raw, &saved) == nil {
			normalizeReport(&saved)
			s.latest = saved
		}
	}
	return s
}

func emptyReport() StorageReport {
	return StorageReport{
		Shares:       make([]StorageShareStat, 0),
		LargestFiles: make([]StorageFileItem, 0),
		OldestFiles:  make([]StorageFileItem, 0),
		Duplicates:   make([]StorageDuplicateGroup, 0),
	}
}

func normalizeReport(r *StorageReport) {
	if r.Shares == nil {
		r.Shares = make([]StorageShareStat, 0)
	}
	for i := range r.Shares {
		if r.Shares[i].TopFolders == nil {
			r.Shares[i].TopFolders = make([]StorageFolderStat, 0)
		}
		if r.Shares[i].Categories == nil {
			r.Shares[i].Categories = make([]StorageCategoryStat, 0)
		}
	}
	if r.LargestFiles == nil {
		r.LargestFiles = make([]StorageFileItem, 0)
	}
	if r.OldestFiles == nil {
		r.OldestFiles = make([]StorageFileItem, 0)
	}
	if r.Duplicates == nil {
		r.Duplicates = make([]StorageDuplicateGroup, 0)
	}
}

// Report returns the latest cached report and live scanning state.
func (s *Service) Report() StorageReport {
	s.mu.RLock()
	out := s.latest
	s.mu.RUnlock()
	out.Scanning = s.scanning.Load()
	return out
}

type fileRec struct {
	shareName string
	relPath   string
	name      string
	size      int64
	modTime   time.Time
}

// Scan walks every share without following symlinks, builds the usage report,
// computes duplicate groups, and persists the snapshot atomically.
func (s *Service) Scan(ctx context.Context) (StorageReport, error) {
	if !s.scanning.CompareAndSwap(false, true) {
		return s.Report(), nil
	}
	defer s.scanning.Store(false)

	start := time.Now()
	shares, err := s.listShares(ctx)
	if err != nil {
		return StorageReport{}, err
	}

	report := emptyReport()
	allFiles := make([]fileRec, 0, 512)
	bySize := make(map[int64][]fileRec)

	for _, sh := range shares {
		if ctx.Err() != nil {
			return StorageReport{}, ctx.Err()
		}
		stat := StorageShareStat{
			ShareID:    sh.ID,
			ShareName:  sh.Name,
			Path:       sh.Path,
			TopFolders: make([]StorageFolderStat, 0),
			Categories: make([]StorageCategoryStat, 0),
		}
		if sh.Path == "" {
			report.Shares = append(report.Shares, stat)
			continue
		}

		folderTotals := make(map[string]*StorageFolderStat)
		catTotals := make(map[string]*StorageCategoryStat)

		// The agent walks the share: the web server cannot read the folders.
		err := s.files.ScanShare(ctx, sh.Name, func(e sys.ShareEntry) error {
			if e.IsDir {
				stat.DirCount++
				report.TotalDirs++
				return nil
			}
			rel, name, sz := e.Path, path.Base(e.Path), e.Size

			stat.FileCount++
			stat.TotalBytes += sz
			report.TotalFiles++
			report.TotalBytes += sz

			// Attribute size to top-level folder (or "(root)").
			top := "(root)"
			topPath := ""
			if idx := strings.IndexByte(rel, '/'); idx > 0 {
				top = rel[:idx]
				topPath = top
			}
			fStat := folderTotals[top]
			if fStat == nil {
				fStat = &StorageFolderStat{Name: top, Path: topPath}
				folderTotals[top] = fStat
			}
			fStat.Bytes += sz
			fStat.Files++

			// Attribute size to category.
			cat := classifyExt(strings.ToLower(path.Ext(name)))
			cStat := catTotals[cat]
			if cStat == nil {
				cStat = &StorageCategoryStat{Category: cat}
				catTotals[cat] = cStat
			}
			cStat.Bytes += sz
			cStat.Files++

			rec := fileRec{
				shareName: sh.Name,
				relPath:   rel,
				name:      name,
				size:      sz,
				modTime:   time.Unix(e.ModTime, 0).UTC(),
			}
			allFiles = append(allFiles, rec)
			if sz >= minDupBytes && sz <= maxDupFileBytes {
				bySize[sz] = append(bySize[sz], rec)
			}
			return nil
		})
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return StorageReport{}, ctx.Err()
		case errors.Is(err, sys.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
			// Not live (no one may use it) or no folder yet: nothing to count.
		default:
			slog.WarnContext(ctx, "storage: could not scan a share", "share", sh.Name, "err", err)
		}

		for _, f := range folderTotals {
			stat.TopFolders = append(stat.TopFolders, *f)
		}
		sort.Slice(stat.TopFolders, func(i, j int) bool { return stat.TopFolders[i].Bytes > stat.TopFolders[j].Bytes })
		if len(stat.TopFolders) > maxTopFolders {
			stat.TopFolders = stat.TopFolders[:maxTopFolders]
		}

		for _, c := range catTotals {
			stat.Categories = append(stat.Categories, *c)
		}
		sort.Slice(stat.Categories, func(i, j int) bool { return stat.Categories[i].Bytes > stat.Categories[j].Bytes })

		report.Shares = append(report.Shares, stat)
	}

	sort.Slice(report.Shares, func(i, j int) bool { return report.Shares[i].TotalBytes > report.Shares[j].TotalBytes })

	// Largest files.
	sort.Slice(allFiles, func(i, j int) bool { return allFiles[i].size > allFiles[j].size })
	for i := 0; i < len(allFiles) && i < maxTopFiles; i++ {
		f := allFiles[i]
		report.LargestFiles = append(report.LargestFiles, StorageFileItem{
			ShareName: f.shareName, Path: f.relPath, Name: f.name, Size: f.size, ModTime: f.modTime.Format(time.RFC3339),
		})
	}

	// Oldest non-empty files.
	sort.Slice(allFiles, func(i, j int) bool { return allFiles[i].modTime.Before(allFiles[j].modTime) })
	for i := 0; i < len(allFiles) && len(report.OldestFiles) < maxTopFiles; i++ {
		f := allFiles[i]
		if f.size == 0 {
			continue
		}
		report.OldestFiles = append(report.OldestFiles, StorageFileItem{
			ShareName: f.shareName, Path: f.relPath, Name: f.name, Size: f.size, ModTime: f.modTime.Format(time.RFC3339),
		})
	}

	// Duplicate detection: hash only size collisions, largest sizes first.
	collisionSizes := make([]int64, 0)
	for sz, list := range bySize {
		if len(list) >= 2 {
			collisionSizes = append(collisionSizes, sz)
		}
	}
	sort.Slice(collisionSizes, func(i, j int) bool { return collisionSizes[i] > collisionSizes[j] })

	hashed := 0
	for _, sz := range collisionSizes {
		if ctx.Err() != nil || hashed >= maxDupHashFiles || len(report.Duplicates) >= maxDuplicateGrps {
			break
		}
		list := bySize[sz]
		if room := maxDupHashFiles - hashed; len(list) > room {
			list = list[:room]
		}
		hashed += len(list)
		byHash := s.hashAll(ctx, list)
		for sum, grp := range byHash {
			if len(grp) < 2 {
				continue
			}
			files := make([]StorageDuplicateFile, 0, len(grp))
			for _, g := range grp {
				files = append(files, StorageDuplicateFile{
					ShareName: g.shareName, Path: g.relPath, ModTime: g.modTime.Format(time.RFC3339),
				})
			}
			reclaim := sz * int64(len(grp)-1)
			report.ReclaimableBytes += reclaim
			report.Duplicates = append(report.Duplicates, StorageDuplicateGroup{
				SHA256:           sum,
				Size:             sz,
				ReclaimableBytes: reclaim,
				Files:            files,
			})
		}
	}
	sort.Slice(report.Duplicates, func(i, j int) bool {
		return report.Duplicates[i].ReclaimableBytes > report.Duplicates[j].ReclaimableBytes
	})
	if len(report.Duplicates) > maxDuplicateGrps {
		report.Duplicates = report.Duplicates[:maxDuplicateGrps]
	}

	report.ScannedAt = time.Now().UTC().Format(time.RFC3339)
	report.DurationMs = time.Since(start).Milliseconds()

	s.mu.Lock()
	s.latest = report
	s.mu.Unlock()

	if raw, err := json.Marshal(report); err == nil {
		tmp := s.reportPath + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, s.reportPath)
		}
	}
	slog.InfoContext(ctx, "storage scan complete",
		"shares", len(report.Shares), "files", report.TotalFiles,
		"bytes", report.TotalBytes, "duplicates", len(report.Duplicates), "duration_ms", report.DurationMs)
	return report, nil
}

// RunTask adapts Scan for the scheduled task runner.
func (s *Service) RunTask(ctx context.Context) error {
	_, err := s.Scan(ctx)
	return err
}

// hashAll hashes files (in the agent, which can read them) and groups them
// by content. Files that could not be hashed are left out.
func (s *Service) hashAll(ctx context.Context, recs []fileRec) map[string][]fileRec {
	byShare := make(map[string][]fileRec)
	for _, r := range recs {
		byShare[r.shareName] = append(byShare[r.shareName], r)
	}
	out := make(map[string][]fileRec)
	for share, list := range byShare {
		for len(list) > 0 && ctx.Err() == nil {
			batch := list[:min(len(list), sys.MaxStatFiles)]
			list = list[len(batch):]
			paths := make([]string, len(batch))
			for i, r := range batch {
				paths[i] = r.relPath
			}
			files, err := s.files.StatFiles(ctx, share, paths, true)
			if err != nil {
				slog.WarnContext(ctx, "storage: could not hash files", "share", share, "err", err)
				break
			}
			sums := make(map[string]string, len(files))
			for _, f := range files {
				if f.SHA256 != "" {
					sums[f.Path] = f.SHA256
				}
			}
			for _, r := range batch {
				if sum, ok := sums[r.relPath]; ok {
					out[sum] = append(out[sum], r)
				}
			}
		}
	}
	return out
}

func classifyExt(ext string) string {
	switch ext {
	case ".mp4", ".mkv", ".mov", ".avi", ".webm", ".m4v", ".wmv", ".flv":
		return "Video"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".heic", ".avif", ".raw", ".dng", ".tiff":
		return "Images"
	case ".mp3", ".flac", ".wav", ".aac", ".ogg", ".m4a", ".opus", ".wma":
		return "Audio"
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".csv", ".txt", ".md", ".rtf":
		return "Documents"
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z", ".rar", ".zst", ".iso", ".dmg", ".img":
		return "Archives"
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".c", ".cpp", ".h", ".java", ".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".css", ".sql", ".sh":
		return "Code"
	default:
		return "Other"
	}
}
