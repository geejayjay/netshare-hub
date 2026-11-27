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
	ScannedAt  string                `json:"scannedAt,omitempty"`
	TotalBytes int64                 `json:"totalBytes"`
	FileCount  int64                 `json:"fileCount"`
	DirCount   int64                 `json:"dirCount"`
	Skipped    bool                  `json:"skipped,omitempty"`
	SkipReason string                `json:"skipReason,omitempty"`
	TopFolders []StorageFolderStat   `json:"topFolders" nullable:"false"`
	Categories []StorageCategoryStat `json:"categories" nullable:"false"`
}

// StorageScanConfig holds persisted share and folder exclusions for scans.
type StorageScanConfig struct {
	ExcludeShares []string `json:"excludeShares" nullable:"false"`
	ExcludePaths  []string `json:"excludePaths" nullable:"false"`
}

// StorageScanRequest is the optional body for POST /storage/scan.
type StorageScanRequest struct {
	Shares        []string `json:"shares,omitempty" doc:"Optional list of share names to scan. When omitted, all non-excluded shares are scanned."`
	ExcludeShares []string `json:"excludeShares,omitempty" doc:"Optional updated list of excluded share names (persisted when provided)."`
	ExcludePaths  []string `json:"excludePaths,omitempty" doc:"Optional updated list of excluded folder/path patterns (persisted when provided)."`
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
	ScanningShare    string                  `json:"scanningShare,omitempty"`
	ScannedFiles     int64                   `json:"scannedFiles,omitempty"`
	ScannedBytes     int64                   `json:"scannedBytes,omitempty"`
	ExcludeShares    []string                `json:"excludeShares" nullable:"false"`
	ExcludePaths     []string                `json:"excludePaths" nullable:"false"`
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
	configPath string
	listShares func(ctx context.Context) ([]ShareRef, error)
	files      Files

	scanning      atomic.Bool
	scannedFiles  atomic.Int64
	scannedBytes  atomic.Int64
	mu            sync.RWMutex
	scanningShare string
	config        StorageScanConfig
	latest        StorageReport
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
		configPath: filepath.Join(dataDir, "storage-config.json"),
		listShares: listShares,
		files:      files,
		config:     StorageScanConfig{ExcludeShares: []string{}, ExcludePaths: []string{}},
		latest:     emptyReport(),
	}
	if raw, err := os.ReadFile(s.configPath); err == nil {
		var savedCfg StorageScanConfig
		if json.Unmarshal(raw, &savedCfg) == nil {
			s.config = normalizeConfig(savedCfg)
		}
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

func normalizeConfig(c StorageScanConfig) StorageScanConfig {
	out := StorageScanConfig{
		ExcludeShares: make([]string, 0, len(c.ExcludeShares)),
		ExcludePaths:  make([]string, 0, len(c.ExcludePaths)),
	}
	seenS := make(map[string]struct{}, len(c.ExcludeShares))
	for _, s := range c.ExcludeShares {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := strings.ToLower(s)
		if _, ok := seenS[k]; !ok {
			seenS[k] = struct{}{}
			out.ExcludeShares = append(out.ExcludeShares, s)
		}
	}
	seenP := make(map[string]struct{}, len(c.ExcludePaths))
	for _, p := range c.ExcludePaths {
		p = strings.Trim(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"), "/")
		if p == "" {
			continue
		}
		k := strings.ToLower(p)
		if _, ok := seenP[k]; !ok {
			seenP[k] = struct{}{}
			out.ExcludePaths = append(out.ExcludePaths, p)
		}
	}
	return out
}

func emptyReport() StorageReport {
	return StorageReport{
		ExcludeShares: make([]string, 0),
		ExcludePaths:  make([]string, 0),
		Shares:        make([]StorageShareStat, 0),
		LargestFiles:  make([]StorageFileItem, 0),
		OldestFiles:   make([]StorageFileItem, 0),
		Duplicates:    make([]StorageDuplicateGroup, 0),
	}
}

func normalizeReport(r *StorageReport) {
	if r.ExcludeShares == nil {
		r.ExcludeShares = make([]string, 0)
	}
	if r.ExcludePaths == nil {
		r.ExcludePaths = make([]string, 0)
	}
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

// Config returns the persisted scan exclusion settings.
func (s *Service) Config() StorageScanConfig {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	return normalizeConfig(cfg)
}

// SetConfig saves new share/path exclusions atomically.
func (s *Service) SetConfig(cfg StorageScanConfig) StorageScanConfig {
	norm := normalizeConfig(cfg)
	s.mu.Lock()
	s.config = norm
	s.mu.Unlock()
	if raw, err := json.Marshal(norm); err == nil {
		tmp := s.configPath + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, s.configPath)
		}
	}
	return norm
}

// Report returns the latest cached report and live scanning state.
func (s *Service) Report() StorageReport {
	s.mu.RLock()
	out := s.latest
	cfg := s.config
	curShare := s.scanningShare
	s.mu.RUnlock()
	out.Scanning = s.scanning.Load()
	out.ExcludeShares = append([]string{}, cfg.ExcludeShares...)
	out.ExcludePaths = append([]string{}, cfg.ExcludePaths...)
	if out.Scanning {
		out.ScanningShare = curShare
		out.ScannedFiles = s.scannedFiles.Load()
		out.ScannedBytes = s.scannedBytes.Load()
	}
	return out
}

// StartScan launches a background storage scan (detached from the HTTP request
// so large shares never time out) and immediately returns the live report state.
func (s *Service) StartScan(req *StorageScanRequest) StorageReport {
	var targetShares []string
	if req != nil {
		if req.ExcludeShares != nil || req.ExcludePaths != nil {
			cur := s.Config()
			if req.ExcludeShares != nil {
				cur.ExcludeShares = req.ExcludeShares
			}
			if req.ExcludePaths != nil {
				cur.ExcludePaths = req.ExcludePaths
			}
			s.SetConfig(cur)
		}
		targetShares = req.Shares
	}
	if !s.scanning.CompareAndSwap(false, true) {
		return s.Report()
	}
	s.scannedFiles.Store(0)
	s.scannedBytes.Store(0)
	s.mu.Lock()
	s.scanningShare = ""
	s.mu.Unlock()
	go func() {
		if _, err := s.runScanLocked(context.Background(), targetShares); err != nil {
			slog.Warn("storage: background scan failed", "err", err)
		}
	}()
	return s.Report()
}

func isPathExcluded(shareName, relPath string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	relLower := strings.ToLower(strings.Trim(relPath, "/"))
	fullLower := strings.ToLower(shareName) + "/" + relLower
	segs := strings.Split(relLower, "/")
	for _, pat := range patterns {
		p := strings.ToLower(pat)
		if p == "" {
			continue
		}
		// Match full "ShareName/rel/path" prefix
		if fullLower == p || strings.HasPrefix(fullLower, p+"/") {
			return true
		}
		// Match relative path prefix inside any share
		if relLower == p || strings.HasPrefix(relLower, p+"/") {
			return true
		}
		// Match any single path segment (e.g. "node_modules", ".git", "qbittorrent")
		if !strings.Contains(p, "/") {
			for _, seg := range segs {
				if seg == p {
					return true
				}
			}
		}
	}
	return false
}

type fileRec struct {
	shareName string
	relPath   string
	name      string
	size      int64
	modTime   time.Time
}

// Scan walks every non-excluded share without following symlinks, builds the
// usage report, computes duplicate groups, and persists the snapshot atomically.
func (s *Service) Scan(ctx context.Context) (StorageReport, error) {
	return s.ScanShares(ctx, nil)
}

// ScanShares synchronously scans either all non-excluded shares (when targetShares
// is empty) or only the named shares, merging targeted results into the latest report.
func (s *Service) ScanShares(ctx context.Context, targetShares []string) (StorageReport, error) {
	if !s.scanning.CompareAndSwap(false, true) {
		return s.Report(), nil
	}
	s.scannedFiles.Store(0)
	s.scannedBytes.Store(0)
	return s.runScanLocked(ctx, targetShares)
}

func (s *Service) runScanLocked(ctx context.Context, targetShares []string) (StorageReport, error) {
	defer func() {
		s.mu.Lock()
		s.scanningShare = ""
		s.mu.Unlock()
		s.scanning.Store(false)
	}()

	start := time.Now()
	nowRFC := start.UTC().Format(time.RFC3339)
	cfg := s.Config()
	excludedShareSet := make(map[string]struct{}, len(cfg.ExcludeShares))
	for _, name := range cfg.ExcludeShares {
		excludedShareSet[strings.ToLower(name)] = struct{}{}
	}
	targetSet := make(map[string]struct{}, len(targetShares))
	for _, name := range targetShares {
		if t := strings.TrimSpace(name); t != "" {
			targetSet[strings.ToLower(t)] = struct{}{}
		}
	}
	targeted := len(targetSet) > 0

	s.mu.RLock()
	prev := s.latest
	s.mu.RUnlock()
	prevByShare := make(map[string]StorageShareStat, len(prev.Shares))
	for _, ps := range prev.Shares {
		prevByShare[strings.ToLower(ps.ShareName)] = ps
	}

	shares, err := s.listShares(ctx)
	if err != nil {
		return StorageReport{}, err
	}

	report := emptyReport()
	report.ExcludeShares = append([]string{}, cfg.ExcludeShares...)
	report.ExcludePaths = append([]string{}, cfg.ExcludePaths...)
	allFiles := make([]fileRec, 0, 512)
	bySize := make(map[int64][]fileRec)

	for _, sh := range shares {
		if ctx.Err() != nil {
			return StorageReport{}, ctx.Err()
		}
		key := strings.ToLower(sh.Name)
		if targeted {
			if _, ok := targetSet[key]; !ok {
				if existing, hasPrev := prevByShare[key]; hasPrev {
					existing.ShareID = sh.ID
					existing.Path = sh.Path
					report.Shares = append(report.Shares, existing)
				} else {
					report.Shares = append(report.Shares, StorageShareStat{
						ShareID:    sh.ID,
						ShareName:  sh.Name,
						Path:       sh.Path,
						Skipped:    true,
						SkipReason: "Not scanned yet",
						TopFolders: make([]StorageFolderStat, 0),
						Categories: make([]StorageCategoryStat, 0),
					})
				}
				continue
			}
		}

		stat := StorageShareStat{
			ShareID:    sh.ID,
			ShareName:  sh.Name,
			Path:       sh.Path,
			TopFolders: make([]StorageFolderStat, 0),
			Categories: make([]StorageCategoryStat, 0),
		}
		if !targeted {
			if _, excluded := excludedShareSet[key]; excluded {
				stat.Skipped = true
				stat.SkipReason = "Excluded from scan"
				report.Shares = append(report.Shares, stat)
				continue
			}
		}
		if sh.Path == "" {
			stat.Skipped = true
			stat.SkipReason = "Share has no folder path"
			report.Shares = append(report.Shares, stat)
			continue
		}

		s.mu.Lock()
		s.scanningShare = sh.Name
		s.mu.Unlock()

		folderTotals := make(map[string]*StorageFolderStat)
		catTotals := make(map[string]*StorageCategoryStat)

		// The agent walks the share: the web server cannot read the folders.
		err := s.files.ScanShare(ctx, sh.Name, func(e sys.ShareEntry) error {
			if isPathExcluded(sh.Name, e.Path, cfg.ExcludePaths) {
				return nil
			}
			if e.IsDir {
				stat.DirCount++
				return nil
			}
			rel, name, sz := e.Path, path.Base(e.Path), e.Size

			stat.FileCount++
			stat.TotalBytes += sz
			s.scannedFiles.Add(1)
			s.scannedBytes.Add(sz)

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
			stat.ScannedAt = nowRFC
		case ctx.Err() != nil:
			return StorageReport{}, ctx.Err()
		case errors.Is(err, sys.ErrNotFound):
			stat.Skipped = true
			stat.SkipReason = "Share is not live (assign at least one user or enable guest access)"
		case errors.Is(err, fs.ErrNotExist):
			stat.Skipped = true
			stat.SkipReason = "Share folder does not exist on disk"
		default:
			stat.Skipped = true
			stat.SkipReason = err.Error()
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

	for _, st := range report.Shares {
		if st.Skipped {
			continue
		}
		report.TotalBytes += st.TotalBytes
		report.TotalFiles += st.FileCount
		report.TotalDirs += st.DirCount
	}
	sort.Slice(report.Shares, func(i, j int) bool { return report.Shares[i].TotalBytes > report.Shares[j].TotalBytes })

	// Largest files (merge untouched shares when running a targeted scan).
	if targeted {
		for _, f := range prev.LargestFiles {
			if _, replaced := targetSet[strings.ToLower(f.ShareName)]; !replaced {
				report.LargestFiles = append(report.LargestFiles, f)
			}
		}
	}
	sort.Slice(allFiles, func(i, j int) bool { return allFiles[i].size > allFiles[j].size })
	for i := 0; i < len(allFiles) && i < maxTopFiles; i++ {
		f := allFiles[i]
		report.LargestFiles = append(report.LargestFiles, StorageFileItem{
			ShareName: f.shareName, Path: f.relPath, Name: f.name, Size: f.size, ModTime: f.modTime.Format(time.RFC3339),
		})
	}
	sort.Slice(report.LargestFiles, func(i, j int) bool { return report.LargestFiles[i].Size > report.LargestFiles[j].Size })
	if len(report.LargestFiles) > maxTopFiles {
		report.LargestFiles = report.LargestFiles[:maxTopFiles]
	}

	// Oldest non-empty files.
	if targeted {
		for _, f := range prev.OldestFiles {
			if _, replaced := targetSet[strings.ToLower(f.ShareName)]; !replaced {
				report.OldestFiles = append(report.OldestFiles, f)
			}
		}
	}
	sort.Slice(allFiles, func(i, j int) bool { return allFiles[i].modTime.Before(allFiles[j].modTime) })
	for i := 0; i < len(allFiles) && i < maxTopFiles; i++ {
		f := allFiles[i]
		if f.size == 0 {
			continue
		}
		report.OldestFiles = append(report.OldestFiles, StorageFileItem{
			ShareName: f.shareName, Path: f.relPath, Name: f.name, Size: f.size, ModTime: f.modTime.Format(time.RFC3339),
		})
	}
	sort.Slice(report.OldestFiles, func(i, j int) bool { return report.OldestFiles[i].ModTime < report.OldestFiles[j].ModTime })
	if len(report.OldestFiles) > maxTopFiles {
		report.OldestFiles = report.OldestFiles[:maxTopFiles]
	}

	// Duplicate detection: hash only size collisions, largest sizes first.
	if targeted {
		for _, grp := range prev.Duplicates {
			touchesTarget := false
			for _, f := range grp.Files {
				if _, ok := targetSet[strings.ToLower(f.ShareName)]; ok {
					touchesTarget = true
					break
				}
			}
			if !touchesTarget {
				report.Duplicates = append(report.Duplicates, grp)
				report.ReclaimableBytes += grp.ReclaimableBytes
			}
		}
	}
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
