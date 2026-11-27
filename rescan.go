package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"slices"
	"strings"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/files"

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
// item whose original is complete in the target and not on the drive, and of
// several copies of one file only for the newest. Existing records are never
// overwritten, and no original is downloaded.
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
	// The files the drive already has a record of: one is not recorded twice.
	records, unusable, err := a.records(ctx)
	if err != nil {
		return res, err
	}
	// Such a record is left alone, and its item cannot be rebuilt over it.
	res.Problems = unusable
	archivedAs := map[content][]Record{}
	for _, r := range records {
		archivedAs[r.content()] = append(archivedAs[r.content()], r)
	}
	// Newest first (ids sort by time), so of several copies the newest is taken.
	slices.SortFunc(entries, func(x, y files.Entry) int { return strings.Compare(y.Name, x.Name) })
	recorded := map[string]bool{}
	for _, e := range entries {
		id, isRecord := strings.CutSuffix(e.Name, ".json")
		if e.IsDir || !isRecord {
			continue
		}
		recorded[id] = true
		p := dir + "/" + e.Name
		rec, orphan, err := a.rebuild(ctx, id, p, archivedAs)
		switch {
		case ctx.Err() != nil:
			return res, ctx.Err()
		case err != nil:
			res.Problems = append(res.Problems, Problem{p, err})
		case orphan != "":
			res.Orphans = append(res.Orphans, Orphan{p, orphan})
		case rec != nil:
			archivedAs[rec.content()] = append(archivedAs[rec.content()], *rec)
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

// content is what makes two records records of the same file.
type content struct {
	sha256, path string
	size         int64
}

func (r Record) content() content { return content{r.SHA256, r.OriginalPath, r.Size} }

// rebuild recreates the drive record of the item id from the target's record at
// p, if it is missing and may be. A reason means the item is an orphan.
func (a *Archive) rebuild(ctx context.Context, id, p string, archivedAs map[content][]Record) (rec *Record, orphan string, err error) {
	if !validID(id) {
		return nil, "the record's name is not an item id", nil
	}
	if known, err := a.known(ctx, id); known || err != nil {
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
	unlock, err := a.lock(ctx, r.OriginalPath)
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	// Again, now that nothing else is working on the file.
	if known, err := a.known(ctx, id); known || err != nil {
		return nil, "", err
	}
	for _, other := range archivedAs[r.content()] {
		stands, err := a.stands(ctx, other)
		if err != nil {
			return nil, "", err
		}
		if stands {
			return nil, "already archived as " + other.ID, nil
		}
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
	r.Rebuilt = true
	if err := a.createDriveRecord(ctx, &r); errors.Is(err, odraz.ErrExists) {
		return nil, "", nil // written meanwhile, so already known
	} else if err != nil {
		return nil, "", err
	}
	return &r, "", nil
}

// known reports whether the drive has a record of the item id.
func (a *Archive) known(ctx context.Context, id string) (bool, error) {
	_, err := a.src.FS.Stat(ctx, RecordPath(id))
	if errors.Is(err, odraz.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// stands reports whether the drive still has the record and, where this target
// can tell, its copy is there in full.
func (a *Archive) stands(ctx context.Context, r Record) (bool, error) {
	if known, err := a.known(ctx, r.ID); !known || err != nil {
		return false, err
	}
	if r.Target.TargetDescriptor != a.dst.Descriptor {
		return true, nil
	}
	obj, err := a.dst.FS.Stat(ctx, r.Target.Key)
	if errors.Is(err, odraz.ErrNotFound) {
		return false, nil
	}
	return err == nil && !obj.IsDir && obj.Size == r.Size, err
}

// createDriveRecord writes a rebuilt record with a placeholder thumbnail,
// replacing nothing: a thumbnail that is already there stays, and a record that
// is already there is odraz.ErrExists.
func (a *Archive) createDriveRecord(ctx context.Context, r *Record) error {
	if err := a.src.FS.Mkdir(ctx, Dir); err != nil {
		return err
	}
	create := func(p string, data []byte) error {
		return a.src.FS.Write(ctx, p, bytes.NewReader(data), files.WriteOptions{Size: int64(len(data))})
	}
	placeholder := media.Placeholder(media.KindOf(path.Base(r.OriginalPath), r.Mime))
	ours := Thumb{File: r.ID + ".jpg", W: placeholder.W, H: placeholder.H, Placeholder: true}
	err := create(ThumbPath(r.ID), placeholder.JPEG)
	switch {
	case errors.Is(err, odraz.ErrExists):
		same, err := a.holds(ctx, ThumbPath(r.ID), placeholder.JPEG)
		if err != nil {
			return err
		}
		if same {
			r.Thumb = ours
		} // else it is the one the record from the target describes
	case err != nil:
		return err
	default:
		r.Thumb = ours
	}
	data, err := r.Marshal()
	if err != nil {
		return err
	}
	return create(RecordPath(r.ID), data) // last: it marks the item as archived
}

// holds reports whether the drive file at p has exactly the content data.
func (a *Archive) holds(ctx context.Context, p string, data []byte) (bool, error) {
	rc, err := a.src.FS.Read(ctx, p, 0, -1)
	if err != nil {
		return false, err
	}
	defer rc.Close()
	got, err := io.ReadAll(io.LimitReader(rc, int64(len(data))+1))
	return bytes.Equal(got, data), err
}
