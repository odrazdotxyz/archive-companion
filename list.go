package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/files"
)

// Problem is a record that could not be used.
type Problem struct {
	Path string
	Err  error
}

type cached struct {
	version string // of the file the record was read from
	record  Record
}

func fileVersion(e files.Entry) string {
	return e.ETag + "|" + e.ModTime.UTC().Format(time.RFC3339Nano)
}

func (r Record) when() time.Time {
	if r.TakenAt != nil {
		return *r.TakenAt
	}
	return r.ModTime
}

// records reads the drive records, newest first. Records already read and not
// changed since are not read again. A file that is not a valid record is a
// Problem; failing to read is an error.
func (a *Archive) records(ctx context.Context) ([]Record, []Problem, error) {
	entries, err := a.src.FS.List(ctx, Dir)
	if errors.Is(err, odraz.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []Record
	var problems []Problem
	for _, e := range entries {
		id, isRecord := strings.CutSuffix(e.Name, ".json")
		if e.IsDir || !isRecord {
			continue
		}
		p := Dir + "/" + e.Name
		a.mu.Lock()
		c, ok := a.cache[e.Name]
		a.mu.Unlock()
		if !ok || c.version != fileVersion(e) {
			data, err := a.readSmall(ctx, a.src.FS, p)
			if errors.Is(err, odraz.ErrNotFound) {
				continue // removed meanwhile
			}
			if err != nil {
				return nil, nil, err
			}
			rec, err := ParseRecord(data, a.src.DriveKey)
			if err == nil && rec.ID != id {
				err = ErrBadRecord
			}
			if err != nil {
				problems = append(problems, Problem{p, err})
				continue
			}
			c = cached{fileVersion(e), rec}
			a.mu.Lock()
			a.cache[e.Name] = c
			a.mu.Unlock()
		}
		out = append(out, c.record)
	}
	slices.SortStableFunc(out, func(x, y Record) int { return y.when().Compare(x.when()) })
	return out, problems, nil
}

// readSmall reads a record file, up to a little more than a record may be.
func (a *Archive) readSmall(ctx context.Context, fs files.FS, p string) ([]byte, error) {
	rc, err := fs.Read(ctx, p, 0, -1)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxRecord+1))
}

// List returns the archived items of the drive, newest first (by the date the
// picture was taken, else the file's). Records that cannot be used are returned
// as problems.
func (a *Archive) List(ctx context.Context) ([]Record, []Problem, error) { return a.records(ctx) }

// Thumbnail returns the thumbnail (a JPEG) of the item id.
func (a *Archive) Thumbnail(ctx context.Context, id string) (io.ReadCloser, error) {
	if !validID(id) {
		return nil, fmt.Errorf("%w: id %q", ErrBadRecord, id)
	}
	return a.src.FS.Read(ctx, ThumbPath(id), 0, -1)
}

// lockItem is load with the item's file locked: the record is read again once
// the lock is held, as it may have gone while waiting.
func (a *Archive) lockItem(ctx context.Context, id string) (Record, func(), error) {
	rec, err := a.load(ctx, id)
	if err != nil {
		return Record{}, nil, err
	}
	unlock, err := a.lock(ctx, rec.OriginalPath)
	if err != nil {
		return Record{}, nil, err
	}
	if rec, err = a.load(ctx, id); err != nil {
		unlock()
		return Record{}, nil, err
	}
	return rec, unlock, nil
}

// load reads the record of the item id and checks that its original is in the
// connected target.
func (a *Archive) load(ctx context.Context, id string) (Record, error) {
	if !validID(id) {
		return Record{}, fmt.Errorf("%w: id %q", ErrBadRecord, id)
	}
	data, err := a.readSmall(ctx, a.src.FS, RecordPath(id))
	if err != nil {
		return Record{}, err
	}
	rec, err := ParseRecord(data, a.src.DriveKey)
	if err != nil {
		return Record{}, err
	}
	if rec.ID != id {
		return Record{}, fmt.Errorf("%w: the record of %s describes %s", ErrBadRecord, id, rec.ID)
	}
	if rec.Target.TargetDescriptor != a.dst.Descriptor {
		return Record{}, fmt.Errorf("%w: %s, bucket %s", ErrWrongTarget, rec.Target.Endpoint, rec.Target.Bucket)
	}
	return rec, nil
}
