package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"time"

	"netshare-hub/internal/modules/jobs"
	"netshare-hub/internal/platform/errs"
)

func errorsIsExist(err error) bool    { return errors.Is(err, fs.ErrExist) }
func errorsIsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// walk visits everything inside dir, folders before what they hold, in name
// order (so a resumed job sees the same order). rel is relative to dir.
// Symbolic links are reported, never followed. With skipUnreadable, a
// folder inside that the user may not read is left out instead of ending
// the walk with an error.
func (s *Service) walk(ctx context.Context, account, share, dir string, skipUnreadable bool, visit func(rel string, e Entry) error) error {
	var inner func(rel string) error
	inner = func(rel string) error {
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			page, err := s.files.List(ctx, account, share, path.Join(dir, rel), after, 1000)
			if err != nil {
				if skipUnreadable && rel != "" && errors.Is(err, fs.ErrPermission) {
					return nil
				}
				return err
			}
			for _, e := range page.Entries {
				child := path.Join(rel, e.Name)
				if err := visit(child, e); err != nil {
					return err
				}
				if e.IsDir && !e.IsLink {
					if err := inner(child); err != nil {
						return err
					}
				}
			}
			if page.Next == "" {
				return nil
			}
			after = page.Next
		}
	}
	return inner("")
}

// copyCheckpoint is where a copy job got to. Files are counted in walk
// order; after a restart the first Files are skipped. Targets keeps the
// folder name chosen for each item (it may have been renamed to keep both).
type copyCheckpoint struct {
	Files   int            `json:"files"`
	Targets map[int]string `json:"targets,omitempty"`
}

// RunCopy is the JobCopy handler: copy (or move, between shares) items into
// a folder. It runs as the job's owner, through their background worker.
func (s *Service) RunCopy(ctx context.Context, run *jobs.Run) error {
	var p copyPayload
	if err := run.Payload(&p); err != nil {
		return err
	}
	var cp copyCheckpoint
	run.Checkpoint(&cp)
	if cp.Targets == nil {
		cp.Targets = map[int]string{}
	}
	account, err := s.accountOf(ctx, run.OwnerID())
	if err != nil {
		return err
	}

	// 1. Count, so progress has a total.
	run.Progress(0, 0, "Counting files…")
	var totalBytes int64
	for _, it := range p.Items {
		e, err := s.files.Stat(ctx, account, it.Share, it.Path)
		if err != nil {
			continue // reported when it is copied
		}
		totalBytes += e.Size
		if e.IsDir && !e.IsLink {
			err = s.walk(ctx, account, it.Share, it.Path, false, func(_ string, e Entry) error {
				if !e.IsDir {
					totalBytes += e.Size
				}
				return nil
			})
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}

	// 2. Copy.
	verb := "Copying"
	if p.Move {
		verb = "Moving"
	}
	var (
		index     int   // files seen, in walk order
		doneBytes int64 // for progress
		failed    int
		lastSave  time.Time
	)
	save := func(force bool) {
		if force || time.Since(lastSave) > time.Second {
			lastSave = time.Now()
			if err := run.SaveCheckpoint(cp); err != nil {
				slog.WarnContext(ctx, "could not save job checkpoint", "job", run.ID(), "err", err)
			}
		}
	}
	// copyOne copies one file and reports whether it is now at the
	// destination (false: skipped or failed, so a move keeps the source).
	copyOne := func(fromShare, from, to string, size int64) (bool, error) {
		index++
		if index <= cp.Files {
			doneBytes += size // done before the restart
			return true, nil
		}
		run.Progress(doneBytes, totalBytes, verb+" "+path.Base(from))
		ok, err := s.copyFile(ctx, account, fromShare, from, p.To.Share, to, p.Conflict)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			failed++
			slog.WarnContext(ctx, "file not copied", "job", run.ID(), "share", fromShare, "path", from, "err", err)
		}
		doneBytes += size
		cp.Files = index
		save(false)
		return ok && err == nil, nil
	}

	for i, it := range p.Items {
		e, err := s.files.Stat(ctx, account, it.Share, it.Path)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			continue
		}
		complete := true // everything in this item reached the destination
		switch {
		case e.IsLink:
			complete = false // links are not copied (they may point anywhere)
		case !e.IsDir:
			ok, err := copyOne(it.Share, it.Path, path.Join(p.To.Path, e.Name), e.Size)
			if err != nil {
				return err
			}
			complete = ok
		default:
			target, ok := cp.Targets[i]
			if !ok {
				target, err = s.folderTarget(ctx, account, p.To.Share, path.Join(p.To.Path, e.Name), p.Conflict)
				if err != nil {
					return fail(err, "destination")
				}
				cp.Targets[i] = target
				save(true)
			}
			err := s.walk(ctx, account, it.Share, it.Path, false, func(rel string, e Entry) error {
				switch {
				case e.IsLink:
					complete = false
				case e.IsDir:
					if _, err := s.files.Mkdir(ctx, account, p.To.Share, path.Join(target, rel)); err != nil && !errorsIsExist(err) {
						failed++
						complete = false
					}
				default:
					ok, err := copyOne(it.Share, path.Join(it.Path, rel), path.Join(target, rel), e.Size)
					if err != nil {
						return err
					}
					complete = complete && ok
				}
				return nil
			})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failed++
				complete = false
			}
		}
		// A move takes the source away only when ALL of it arrived.
		if p.Move && complete {
			if err := s.files.Remove(ctx, account, it.Share, it.Path, true); err != nil && !errorsIsNotExist(err) {
				failed++
			}
		}
	}
	save(true)
	run.Progress(totalBytes, totalBytes, "Done.")
	if failed > 0 {
		return errs.Unavailable(CodePartlyDone, fmt.Sprintf(
			"%d items could not be copied (no permission, or they changed meanwhile). Everything else was. Check the logs for details.", failed))
	}
	return nil
}

// folderTarget picks where a copied folder goes: its own name, or with
// "keep both" a free name when that is taken. Replace and skip merge into
// an existing folder (file by file, by the same rule).
func (s *Service) folderTarget(ctx context.Context, account, share, p string, conflict Conflict) (string, error) {
	if conflict == ConflictRename {
		var err error
		if p, err = s.freeName(ctx, account, share, p); err != nil {
			return "", err
		}
	}
	if _, err := s.files.Mkdir(ctx, account, share, p); err != nil && !errorsIsExist(err) {
		return "", err
	}
	return p, nil
}

// copyFile copies one file by the conflict rule. ok is false when the file
// was skipped because the destination already had one.
func (s *Service) copyFile(ctx context.Context, account, fromShare, from, toShare, to string, conflict Conflict) (ok bool, err error) {
	switch conflict {
	case ConflictReplace:
		_, err = s.files.Copy(ctx, account, fromShare, from, toShare, to, true)
		return err == nil, err
	case ConflictSkip:
		_, err = s.files.Copy(ctx, account, fromShare, from, toShare, to, false)
		if errorsIsExist(err) {
			return false, nil
		}
		return err == nil, err
	default:
		for attempt := 0; attempt < 5; attempt++ {
			if to, err = s.freeName(ctx, account, toShare, to); err != nil {
				return false, err
			}
			_, err = s.files.Copy(ctx, account, fromShare, from, toShare, to, false)
			if !errorsIsExist(err) {
				return err == nil, err // done, or failed for another reason
			}
			// Someone took the name meanwhile: pick another.
		}
		return false, err
	}
}

type deleteCheckpoint struct {
	Done int `json:"done"`
}

// RunDelete is the JobDelete handler.
func (s *Service) RunDelete(ctx context.Context, run *jobs.Run) error {
	var p deletePayload
	if err := run.Payload(&p); err != nil {
		return err
	}
	var cp deleteCheckpoint
	run.Checkpoint(&cp)
	account, err := s.accountOf(ctx, run.OwnerID())
	if err != nil {
		return err
	}
	total := int64(len(p.Items))
	failed := 0
	for i := cp.Done; i < len(p.Items); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		it := p.Items[i]
		run.Progress(int64(i), total, "Deleting "+path.Base(it.Path))
		if err := s.files.Remove(ctx, account, it.Share, it.Path, true); err != nil && !errorsIsNotExist(err) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			slog.WarnContext(ctx, "not deleted", "job", run.ID(), "share", it.Share, "path", it.Path, "err", err)
		}
		cp.Done = i + 1
		if err := run.SaveCheckpoint(cp); err != nil {
			slog.WarnContext(ctx, "could not save job checkpoint", "job", run.ID(), "err", err)
		}
	}
	run.Progress(total, total, "Done.")
	if failed > 0 {
		return errs.Unavailable(CodePartlyDone, fmt.Sprintf(
			"%d items could not be deleted (no permission, or something inside is protected). Check the logs for details.", failed))
	}
	return nil
}
