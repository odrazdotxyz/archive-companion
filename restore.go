package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/files"
)

// RestoreInput is what the caller may add to Restore.
type RestoreInput struct {
	// KeepCopy leaves the original in the target.
	KeepCopy bool
	Progress func(s Stage, done, total int64)
}

// RestoreResult says where a restored file is.
type RestoreResult struct {
	Path     string
	CopyKept bool
}

// maxNames is how many "(restored N)" names are tried.
const maxNames = 100

// Restore brings the item id back to where it was, or beside whatever is there
// now: it never overwrites a file. The copy in the target is removed only after
// the restored file has been read back and found right.
func (a *Archive) Restore(ctx context.Context, id string, in RestoreInput) (RestoreResult, error) {
	none := RestoreResult{}
	if a.src.FS.Capabilities().ReadOnly || (!in.KeepCopy && a.dst.FS.Capabilities().ReadOnly) {
		return none, fmt.Errorf("archive: restore needs a writable drive and target: %w", odraz.ErrPermission)
	}
	rec, err := a.load(ctx, id)
	if err != nil {
		return none, err
	}
	progress := func(s Stage, done, total int64) {
		if in.Progress != nil {
			in.Progress(s, done, total)
		}
	}
	dest, there, err := a.destination(ctx, rec)
	if err != nil {
		return none, err
	}
	if !there {
		if err := a.bringBack(ctx, rec, dest, progress); err != nil {
			return none, err
		}
	}
	if !in.KeepCopy {
		progress(Removing, 0, 0)
		if err := a.purgeCopy(ctx, rec); err != nil {
			return none, fmt.Errorf("archive: %s is restored, but its copy could not be removed: %w", dest, err)
		}
	}
	if err := a.removeDriveRecord(ctx, id); err != nil {
		return none, fmt.Errorf("archive: %s is restored, but its record could not be removed: %w", dest, err)
	}
	return RestoreResult{Path: dest, CopyKept: in.KeepCopy}, nil
}

// destination picks the path to restore to. there is true if the file is
// already at that path with the right content, as after an interrupted restore.
func (a *Archive) destination(ctx context.Context, rec Record) (p string, there bool, err error) {
	dir, name := path.Split(rec.OriginalPath)
	ext := path.Ext(name)
	if ext == name { // ".profile" has no extension
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i <= maxNames; i++ {
		switch i {
		case 0:
			p = rec.OriginalPath
		case 1:
			p = dir + stem + " (restored)" + ext
		default:
			p = fmt.Sprintf("%s%s (restored %d)%s", dir, stem, i, ext)
		}
		st, err := a.src.FS.Stat(ctx, p)
		switch {
		case errors.Is(err, odraz.ErrNotFound):
			return p, false, nil
		case err != nil:
			return "", false, err
		case st.IsDir || st.Size != rec.Size:
			continue
		}
		sum, _, err := hashFile(ctx, a.src.FS, p, nil)
		if err != nil {
			return "", false, fmt.Errorf("%w: %s: %w", ErrNotVerified, p, err)
		}
		if sum == rec.SHA256 {
			return p, true, nil
		}
	}
	return "", false, fmt.Errorf("archive: no free name beside %s: %w", rec.OriginalPath, odraz.ErrExists)
}

// bringBack streams the item to dest, which does not exist, and verifies it.
func (a *Archive) bringBack(ctx context.Context, rec Record, dest string, progress func(Stage, int64, int64)) error {
	if dir := path.Dir(dest); dir != "/" {
		if err := a.src.FS.Mkdir(ctx, dir); err != nil {
			return err
		}
	}
	rc, err := a.dst.FS.Read(ctx, rec.Target.Key, 0, -1)
	if err != nil {
		return err
	}
	defer rc.Close()
	// remove undoes the write: dest is this call's file.
	remove := func() {
		c, cancel := cleanup(ctx)
		defer cancel()
		_ = a.src.FS.Delete(c, dest, false)
	}
	progress(Uploading, 0, rec.Size)
	got := sha256.New()
	err = a.src.FS.Write(ctx, dest, io.TeeReader(rc, got), files.WriteOptions{
		Size:     rec.Size,
		Progress: func(done, total int64) { progress(Uploading, done, total) },
	})
	if err != nil {
		if !errors.Is(err, odraz.ErrExists) { // else someone else's file got there first
			remove()
		}
		return err
	}
	if hex.EncodeToString(got.Sum(nil)) != rec.SHA256 {
		remove()
		return fmt.Errorf("%w: the copy in the target is not the file that was archived", ErrNotVerified)
	}
	progress(Verifying, 0, rec.Size)
	sum, n, err := hashFile(ctx, a.src.FS, dest, nil)
	if err != nil {
		// Unknown, not wrong: the file stays, and the next try checks it.
		return fmt.Errorf("%w: reading back %s: %w", ErrNotVerified, dest, err)
	}
	if sum != rec.SHA256 || n != rec.Size {
		remove()
		return fmt.Errorf("%w: %s was not written correctly", ErrNotVerified, dest)
	}
	return nil
}
