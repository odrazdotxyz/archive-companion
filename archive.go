// Package archive moves photos and videos from a self-hosted cloud drive to an
// object store, keeping a thumbnail and a record of each on the drive, and
// brings them back.
//
// The rule everything follows: an original is removed only after its copy has
// been verified and recorded.
package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sync"
	"time"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/files"

	"github.com/odrazdotxyz/archive-companion/media"
)

// Source is the drive files are archived from. DriveKey names it in the target:
// lower-case letters, digits and "-", at most 64, and the same on every device.
type Source struct {
	FS       files.FS
	DriveKey string
}

// Target is where originals are kept. FS must implement files.ObjectStore.
type Target struct {
	FS         files.FS
	Descriptor TargetDescriptor
}

// VerifyMode says how a copy is checked before the original is removed.
type VerifyMode int

const (
	// VerifyChecksum compares with the target's own checksum, without a
	// download, and reads the copy back only where that cannot decide.
	VerifyChecksum VerifyMode = iota
	// VerifyDownload always reads the copy back.
	VerifyDownload
)

// Options of an Archive.
type Options struct {
	Verify VerifyMode
	Now    func() time.Time // for tests
}

// Stage is a step of an operation, reported to progress callbacks.
type Stage string

const (
	Uploading Stage = "uploading"
	Verifying Stage = "verifying"
	Saving    Stage = "saving"
	Removing  Stage = "removing"
)

// ArchiveInput is what the caller may add to Archive.
type ArchiveInput struct {
	// Thumbnail is an image of the file made by the caller (a video frame, say).
	Thumbnail []byte
	Progress  func(s Stage, done, total int64)
}

// Archive archives the files of one drive into one target. Operations on one file
// take turns within a process, Rescan included; an application running in several
// processes, or on several devices, must not run these on the same file at once.
type Archive struct {
	src   Source
	dst   Target
	store files.ObjectStore
	opts  Options

	mu    sync.Mutex
	cache map[string]cached // drive records already read, by file name
}

// New returns the archive of src in dst.
func New(src Source, dst Target, opts Options) (*Archive, error) {
	if !driveKeyRE.MatchString(src.DriveKey) {
		return nil, fmt.Errorf("%w: drive key %q", ErrBadRecord, src.DriveKey)
	}
	store, ok := dst.FS.(files.ObjectStore)
	if !ok {
		return nil, fmt.Errorf("archive: the target cannot verify and purge objects: %w", odraz.ErrUnsupported)
	}
	if err := dst.Descriptor.check(); err != nil {
		return nil, err
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Archive{src: src, dst: dst, store: store, opts: opts, cache: map[string]cached{}}, nil
}

func (a *Archive) writable() error {
	if a.src.FS.Capabilities().ReadOnly {
		return fmt.Errorf("archive: the drive is read-only: %w", odraz.ErrPermission)
	}
	if a.dst.FS.Capabilities().ReadOnly {
		return fmt.Errorf("archive: the target is read-only: %w", odraz.ErrPermission)
	}
	return nil
}

// cleanup returns a context for undoing, which also runs after a cancellation.
func cleanup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
}

func unchanged(a, b files.Entry) bool {
	return a.Size == b.Size && a.ETag == b.ETag && a.ModTime.Equal(b.ModTime) && a.IsDir == b.IsDir
}

// Archive moves the photo or video at p to the target. If it fails the original
// stays, and running it again finishes the job.
func (a *Archive) Archive(ctx context.Context, p string, in ArchiveInput) (Record, error) {
	none := Record{}
	if err := a.writable(); err != nil {
		return none, err
	}
	clean, err := files.CleanPath(p)
	if err != nil || !validPath(clean) {
		return none, fmt.Errorf("archive: %q cannot be archived: %w", p, odraz.ErrInvalidPath)
	}
	unlock, err := a.lock(ctx, clean)
	if err != nil {
		return none, err
	}
	defer unlock()
	st, err := a.src.FS.Stat(ctx, clean)
	if err != nil {
		return none, err
	}
	kind := media.KindOf(path.Base(clean), st.MimeType)
	if st.IsDir || kind == media.None {
		return none, fmt.Errorf("%w: %s", ErrNotMedia, clean)
	}
	progress := func(s Stage, done, total int64) {
		if in.Progress != nil {
			in.Progress(s, done, total)
		}
	}
	if rec, done, err := a.resume(ctx, clean, st, progress); done || err != nil {
		return rec, err
	}

	thumb := a.offeredThumbnail(ctx, clean, in.Thumbnail)
	id := NewID(a.opts.Now())
	key := ObjectKey(a.src.DriveKey, id, path.Base(clean))

	// Everything written to the target so far is removed again on failure.
	undoTarget := func() {
		c, cancel := cleanup(ctx)
		defer cancel()
		a.purgeItem(c, id, key)
	}

	progress(Uploading, 0, st.Size)
	seen, err := a.upload(ctx, clean, key, st, kind, thumb == nil, progress)
	if err != nil {
		undoTarget()
		return none, err
	}
	progress(Verifying, 0, st.Size)
	if err := a.mustVerify(ctx, seen.check, key, seen.sum, st.Size); err != nil {
		undoTarget()
		return none, err
	}
	if thumb == nil {
		thumb = seen.thumbnail(kind)
	}

	progress(Saving, 0, 0)
	info := media.Inspect(seen.head)
	rec := Record{
		Version: RecordVersion, ID: id,
		Target: RecordTarget{TargetDescriptor: a.dst.Descriptor, Key: key},
		SHA256: seen.sum, Size: st.Size, Mime: st.MimeType, OriginalPath: clean, ModTime: st.ModTime.UTC(),
		TakenAt: info.TakenAt, Width: info.Width, Height: info.Height,
		Thumb:      Thumb{File: id + ".jpg", W: thumb.W, H: thumb.H, Placeholder: thumb.Placeholder},
		ArchivedAt: a.opts.Now().UTC(),
	}
	data, err := rec.Marshal()
	if err == nil {
		err = a.writeBucketRecord(ctx, id, data)
	}
	if err != nil {
		undoTarget()
		return none, err
	}
	if err := a.writeDriveRecord(ctx, id, thumb.JPEG, data); err != nil {
		// The write may have happened although it reported failure. The copy in
		// the target goes only once the record is certainly not there.
		c, cancel := cleanup(ctx)
		defer cancel()
		if a.removeDriveRecord(c, id) == nil {
			a.purgeItem(c, id, key)
		}
		return none, err
	}

	progress(Removing, 0, 0)
	// undo takes back the archive of a file that changed meanwhile.
	undo := func() (Record, error) {
		c, cancel := cleanup(ctx)
		defer cancel()
		if err := a.removeDriveRecord(c, id); err != nil {
			return none, errors.Join(ErrSourceChanged, err)
		}
		a.purgeItem(c, id, key)
		return none, fmt.Errorf("%w: %s", ErrSourceChanged, clean)
	}
	now, err := a.src.FS.Stat(ctx, clean)
	switch {
	case errors.Is(err, odraz.ErrNotFound):
		return rec, nil // removed by someone else: the archived copy is the one left
	case err != nil:
		return none, err // the record is there: running again finishes
	case !unchanged(st, now):
		return undo()
	}
	if err := a.deleteOriginal(ctx, clean, st); errors.Is(err, odraz.ErrChanged) {
		return undo()
	} else if err != nil {
		return none, err
	}
	return rec, nil
}

// deleteOriginal removes the file at p, which the caller has just seen to be
// still st. A drive that can delete only if unchanged is asked to, and answers
// odraz.ErrChanged for a file changed since; on any other drive a change after
// that look goes unnoticed. A file already gone is not an error.
func (a *Archive) deleteOriginal(ctx context.Context, p string, st files.Entry) error {
	err := error(odraz.ErrUnsupported)
	if d, ok := a.src.FS.(files.ConditionalDeleter); ok && st.ETag != "" {
		err = d.DeleteIfMatch(ctx, p, st.ETag)
	}
	if errors.Is(err, odraz.ErrUnsupported) {
		err = a.src.FS.Delete(ctx, p, false)
	}
	if errors.Is(err, odraz.ErrNotFound) {
		return nil
	}
	return err
}

// resume finishes an earlier run that recorded the file but did not remove it.
// done is false if there is nothing to finish.
func (a *Archive) resume(ctx context.Context, p string, st files.Entry, progress func(Stage, int64, int64)) (Record, bool, error) {
	records, _, err := a.records(ctx)
	if err != nil {
		return Record{}, false, err
	}
	for _, rec := range records { // newest first
		if rec.OriginalPath != p || rec.Size != st.Size || rec.Target.TargetDescriptor != a.dst.Descriptor {
			continue
		}
		progress(Verifying, 0, rec.Size)
		check := a.store.NewCheck(rec.Size)
		sum, n, err := hashFile(ctx, a.src.FS, p, check)
		if err != nil {
			return Record{}, false, err
		}
		if sum != rec.SHA256 || n != rec.Size {
			continue
		}
		ok, err := a.verify(ctx, check, rec.Target.Key, rec.SHA256, rec.Size)
		if err != nil {
			return Record{}, false, err
		}
		if !ok {
			continue // the copy is damaged or gone: archive the file again
		}
		now, err := a.src.FS.Stat(ctx, p)
		if err != nil {
			return Record{}, false, err
		}
		if !unchanged(st, now) {
			return Record{}, false, fmt.Errorf("%w: %s", ErrSourceChanged, p)
		}
		progress(Removing, 0, 0)
		// The record is why the file may go: it must still be there (another
		// device may have restored or deleted the item meanwhile).
		if _, err := a.src.FS.Stat(ctx, RecordPath(rec.ID)); err != nil {
			return Record{}, false, fmt.Errorf("%w: its record went away: %w", ErrSourceChanged, err)
		}
		if err := a.deleteOriginal(ctx, p, st); errors.Is(err, odraz.ErrChanged) {
			return Record{}, false, fmt.Errorf("%w: %s", ErrSourceChanged, p)
		} else if err != nil {
			return Record{}, false, err
		}
		return rec, true, nil
	}
	return Record{}, false, nil
}

// offeredThumbnail is the caller's image or the server's preview, if usable.
func (a *Archive) offeredThumbnail(ctx context.Context, p string, offered []byte) *media.Image {
	if len(offered) > 0 {
		if img, err := media.Normalize(offered); err == nil {
			return &img
		}
	}
	rc, _, err := files.Preview(ctx, a.src.FS, p, media.LongEdge, media.LongEdge)
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, media.MaxThumbInput+1))
	if err != nil {
		return nil
	}
	img, err := media.Normalize(data)
	if err != nil {
		return nil
	}
	return &img
}

// purgeItem removes what the archive keeps in the target for an item, best
// effort: what stays is reported by Rescan.
func (a *Archive) purgeItem(ctx context.Context, id, key string) {
	_ = a.store.Purge(ctx, BucketRecordPath(a.src.DriveKey, id))
	if a.store.Purge(ctx, key) == nil {
		_ = a.store.Purge(ctx, itemFolder(a.src.DriveKey, id))
	}
}

func (a *Archive) writeBucketRecord(ctx context.Context, id string, data []byte) error {
	if err := a.dst.FS.Mkdir(ctx, bucketRecordDir(a.src.DriveKey)); err != nil {
		return err
	}
	p := BucketRecordPath(a.src.DriveKey, id)
	check := a.store.NewCheck(int64(len(data)))
	err := a.dst.FS.Write(ctx, p, io.TeeReader(bytes.NewReader(data), check), files.WriteOptions{Size: int64(len(data))})
	if err != nil {
		return err
	}
	return a.mustVerify(ctx, check, p, hashBytes(data), int64(len(data)))
}

func (a *Archive) writeDriveRecord(ctx context.Context, id string, thumb, record []byte) error {
	if err := a.src.FS.Mkdir(ctx, Dir); err != nil {
		return err
	}
	put := func(p string, data []byte) error {
		return a.src.FS.Write(ctx, p, bytes.NewReader(data), files.WriteOptions{Size: int64(len(data)), Overwrite: true})
	}
	if err := put(ThumbPath(id), thumb); err != nil {
		return err
	}
	return put(RecordPath(id), record) // last: it marks the item as archived
}

// removeDriveRecord removes the record and then the thumbnail. An error means
// the record may still be there.
func (a *Archive) removeDriveRecord(ctx context.Context, id string) error {
	if err := a.src.FS.Delete(ctx, RecordPath(id), false); err != nil && !errors.Is(err, odraz.ErrNotFound) {
		return err
	}
	_ = a.src.FS.Delete(ctx, ThumbPath(id), false)
	return nil
}
