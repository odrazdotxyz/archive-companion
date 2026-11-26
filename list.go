package archive

import (
	"context"
	"errors"
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
