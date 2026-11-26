package archive

import (
	"context"
	"errors"
	"path"
	"strings"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"

	"github.com/odrazdotxyz/archive-companion/media"
)

// Orphan is something in the target that has no drive record and was not given one.
type Orphan struct {
	Key    string
	Reason string
}

// RescanResult says what Rescan did and found.
type RescanResult struct {
	Rebuilt  []Record
	Orphans  []Orphan
	Problems []Problem
}

// Rescan recreates drive records that are missing from the records kept in the
// target, for example after a server was lost. A record is only recreated for an
// item whose original is complete in the target and not on the drive. Existing
// records are never overwritten, and no original is downloaded.
func (a *Archive) Rescan(ctx context.Context) (RescanResult, error) {
	var res RescanResult
	if err := a.writable(); err != nil {
		return res, err
	}
	dir := bucketRecordDir(a.src.DriveKey)
	entries, err := a.dst.FS.List(ctx, dir)
	if err != nil && !errors.Is(err, odraz.ErrNotFound) {
		return res, err
	}
	recorded := map[string]bool{}
	for _, e := range entries {
		id, isRecord := strings.CutSuffix(e.Name, ".json")
		if e.IsDir || !isRecord {
			continue
		}
		recorded[id] = true
		p := dir + "/" + e.Name
		rec, orphan, err := a.rebuild(ctx, id, p)
		switch {
		case ctx.Err() != nil:
			return res, ctx.Err()
		case err != nil:
			res.Problems = append(res.Problems, Problem{p, err})
		case orphan != "":
			res.Orphans = append(res.Orphans, Orphan{p, orphan})
		case rec != nil:
			res.Rebuilt = append(res.Rebuilt, *rec)
		}
	}

	// Originals whose record never reached the target.
	items, err := a.dst.FS.List(ctx, path.Dir(dir))
	if err != nil && !errors.Is(err, odraz.ErrNotFound) {
		return res, err
	}
	for _, e := range items {
		if !e.IsDir || !validID(e.Name) || recorded[e.Name] {
			continue
		}
		p := itemFolder(a.src.DriveKey, e.Name)
		inside, err := a.dst.FS.List(ctx, p)
		if err != nil && !errors.Is(err, odraz.ErrNotFound) {
			res.Problems = append(res.Problems, Problem{p, err})
			continue
		}
		for _, f := range inside {
			res.Orphans = append(res.Orphans, Orphan{p + "/" + f.Name, "there is no record of it in the target"})
		}
	}
	return res, nil
}

// rebuild recreates the drive record of the item id from the target's record at
// p, if it is missing and may be. A reason means the item is an orphan.
func (a *Archive) rebuild(ctx context.Context, id, p string) (rec *Record, orphan string, err error) {
	if !validID(id) {
		return nil, "the record's name is not an item id", nil
	}
	if _, err := a.src.FS.Stat(ctx, RecordPath(id)); err == nil {
		return nil, "", nil // already known
	} else if !errors.Is(err, odraz.ErrNotFound) {
		return nil, "", err
	}
	data, err := a.readSmall(ctx, a.dst.FS, p)
	if err != nil {
		return nil, "", err
	}
	r, err := ParseRecord(data, a.src.DriveKey)
	switch {
	case err != nil:
		return nil, err.Error(), nil
	case r.ID != id:
		return nil, "the record describes another item", nil
	case r.Target.TargetDescriptor != a.dst.Descriptor:
		return nil, "the record names another target", nil
	}
	obj, err := a.dst.FS.Stat(ctx, r.Target.Key)
	switch {
	case errors.Is(err, odraz.ErrNotFound):
		return nil, "the original is missing from the target", nil
	case err != nil:
		return nil, "", err
	case obj.IsDir || obj.Size != r.Size:
		return nil, "the original in the target has another size", nil
	}
	switch st, err := a.src.FS.Stat(ctx, r.OriginalPath); {
	case err == nil && !st.IsDir:
		return nil, "a file is still on the drive at " + r.OriginalPath, nil
	case err != nil && !errors.Is(err, odraz.ErrNotFound):
		return nil, "", err
	}
	thumb := media.Placeholder(media.KindOf(path.Base(r.OriginalPath), r.Mime))
	r.Rebuilt = true
	r.Thumb = Thumb{File: id + ".jpg", W: thumb.W, H: thumb.H, Placeholder: true}
	data, err = r.Marshal()
	if err != nil {
		return nil, "", err
	}
	if err := a.writeDriveRecord(ctx, id, thumb.JPEG, data); err != nil {
		return nil, "", err
	}
	return &r, "", nil
}
