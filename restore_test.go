package archive_test

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"

	archive "github.com/odrazdotxyz/archive-companion"
)

// archived returns a rig whose photo has been archived.
func archived(t *testing.T) (*rig, archive.Record) {
	t.Helper()
	r := newRig(t, archive.VerifyChecksum)
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	return r, rec
}

func left(r *rig) []string {
	var out []string
	for _, p := range r.src.Files() {
		if strings.HasPrefix(p, archive.Dir) {
			out = append(out, "drive "+p)
		}
	}
	for _, p := range r.dst.Files() {
		out = append(out, "target "+p)
	}
	return out
}

func TestRestoreToOriginalPath(t *testing.T) {
	r, rec := archived(t)
	got, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{})
	if err != nil || got.Path != photoPath || got.CopyKept {
		t.Fatalf("%+v %v", got, err)
	}
	if !bytes.Equal(r.src.Data(photoPath), r.photo) {
		t.Fatal("content differs")
	}
	if l := left(r); len(l) != 0 {
		t.Errorf("left: %v", l)
	}
	if r.dst.Has("/odraz/" + dk + "/" + rec.ID) {
		t.Error("the item folder stays in the target")
	}
}

func TestRestoreCreatesMissingFolders(t *testing.T) {
	r, rec := archived(t)
	for _, p := range []string{"/Fotos/Köln", "/Fotos"} {
		if err := r.src.Delete(ctx, p, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); err != nil || !r.src.Has(photoPath) {
		t.Fatal(err)
	}
}

func TestRestoreNeverOverwrites(t *testing.T) {
	r, rec := archived(t)
	r.src.Put(photoPath, []byte("something newer"))
	r.src.Put("/Fotos/Köln/IMG 1 (restored).png", []byte("also taken"))
	got, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{})
	if err != nil || got.Path != "/Fotos/Köln/IMG 1 (restored 2).png" {
		t.Fatalf("%+v %v", got, err)
	}
	if string(r.src.Data(photoPath)) != "something newer" || string(r.src.Data("/Fotos/Köln/IMG 1 (restored).png")) != "also taken" || !bytes.Equal(r.src.Data(got.Path), r.photo) {
		t.Fatal("a file was overwritten")
	}
}

func TestRestoreNames(t *testing.T) {
	for from, want := range map[string]string{
		"/.hidden.png": "/.hidden (restored).png",
		"/a.b.png":     "/a.b (restored).png",
		"/noext":       "/noext (restored)",
	} {
		r := newRig(t, archive.VerifyChecksum)
		r.src.Put(from, r.photo)
		if from == "/noext" { // only the type says it is a photo
			r.src.SetMime(from, "image/png")
		}
		rec, err := r.a.Archive(ctx, from, archive.ArchiveInput{})
		if err != nil {
			t.Fatal(from, err)
		}
		r.src.Put(from, []byte("occupied"))
		got, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{})
		if err != nil || got.Path != want {
			t.Errorf("%s -> %q, %v", from, got.Path, err)
		}
	}
}

func TestRestoreCorruptObjectLeavesNoFile(t *testing.T) {
	r, rec := archived(t)
	r.dst.Corrupt(rec.Target.Key)
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); !errors.Is(err, archive.ErrNotVerified) {
		t.Fatalf("err = %v", err)
	}
	if r.src.Has(photoPath) || !r.src.Has(archive.RecordPath(rec.ID)) || r.dst.Data(rec.Target.Key) == nil {
		t.Fatalf("left: %v", left(r))
	}
}

func TestRestoreFailedWriteRemovesOnlyItsOwnFile(t *testing.T) {
	r, rec := archived(t)
	r.src.Before = func(op, p string) error {
		if op == "write" && p == photoPath {
			r.src.Put(photoPath, []byte("half"))
			return errors.New("connection reset")
		}
		return nil
	}
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); err == nil {
		t.Fatal("success")
	}
	if r.src.Has(photoPath) {
		t.Fatal("a partial file stays")
	}
	// Someone else creating the file meanwhile is not this call's file.
	r.src.Before = func(op, p string) error {
		if op == "write" && p == photoPath {
			r.src.Put(photoPath, []byte("theirs"))
		}
		return nil
	}
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); !errors.Is(err, odraz.ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if string(r.src.Data(photoPath)) != "theirs" {
		t.Fatal("another's file was removed")
	}
}

func TestRestoreFailedReadBackKeepsObjectAndRecord(t *testing.T) {
	r, rec := archived(t)
	r.src.Before = func(op, p string) error {
		if op == "read" && p == photoPath {
			return errors.New("offline")
		}
		return nil
	}
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); !errors.Is(err, archive.ErrNotVerified) {
		t.Fatalf("err = %v", err)
	}
	if !r.src.Has(archive.RecordPath(rec.ID)) || r.dst.Data(rec.Target.Key) == nil {
		t.Fatalf("left: %v", left(r))
	}
	r.src.Before = nil
	got, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{})
	if err != nil || got.Path != photoPath || len(left(r)) != 0 {
		t.Fatalf("second try: %+v %v %v", got, err, left(r))
	}
}

func TestRestoreResumesAfterEachFailure(t *testing.T) {
	boom := errors.New("boom")
	for k := 1; ; k++ {
		r, rec := archived(t)
		n, failed := 0, ""
		hook := func(op, p string) error {
			if n++; n == k {
				failed = op + " " + p
				return boom
			}
			return nil
		}
		r.src.Before, r.dst.Before = hook, hook
		_, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{})
		r.src.Before, r.dst.Before = nil, nil
		if n < k {
			if k < 8 {
				t.Fatalf("only %d operations", n)
			}
			return
		}
		// The content is always somewhere.
		if !bytes.Equal(r.src.Data(photoPath), r.photo) && !bytes.Equal(r.dst.Data(rec.Target.Key), r.photo) {
			t.Fatalf("failing %s: the photo is nowhere", failed)
		}
		if err != nil && r.src.Has(archive.RecordPath(rec.ID)) {
			if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); err != nil {
				t.Fatalf("failing %s: second try: %v", failed, err)
			}
		}
		if !bytes.Equal(r.src.Data(photoPath), r.photo) || r.src.Has("/Fotos/Köln/IMG 1 (restored).png") {
			t.Fatalf("failing %s: %v", failed, r.src.Files())
		}
	}
}

func TestRestoreKeepCopy(t *testing.T) {
	r, rec := archived(t)
	got, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{KeepCopy: true})
	if err != nil || !got.CopyKept || !bytes.Equal(r.dst.Data(rec.Target.Key), r.photo) || r.dst.Data(archive.BucketRecordPath(dk, rec.ID)) == nil || r.src.Has(archive.RecordPath(rec.ID)) {
		t.Fatalf("%+v %v %v", got, err, left(r))
	}
}

func TestWrongTarget(t *testing.T) {
	r, rec := archived(t)
	other, err := archive.New(archive.Source{FS: r.src, DriveKey: dk}, archive.Target{FS: r.dst, Descriptor: archive.TargetDescriptor{Endpoint: desc.Endpoint, Bucket: "another"}}, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	before := left(r)
	if _, err := other.Restore(ctx, rec.ID, archive.RestoreInput{}); !errors.Is(err, archive.ErrWrongTarget) {
		t.Fatalf("restore: %v", err)
	}
	if err := other.Delete(ctx, rec.ID); !errors.Is(err, archive.ErrWrongTarget) {
		t.Fatalf("delete: %v", err)
	}
	if !slices.Equal(before, left(r)) || r.src.Has(photoPath) {
		t.Fatal("something was touched")
	}
}

func TestTamperedRecordCannotTouchOtherKeys(t *testing.T) {
	r, victim := archived(t)
	r.src.Put("/b.png", r.photo)
	rec, err := r.a.Archive(ctx, "/b.png", archive.ArchiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	rec.Target.Key = victim.Target.Key // points at another item's original
	data, _ := rec.Marshal()
	r.src.Put(archive.RecordPath(rec.ID), data)
	before := len(r.dst.Log())
	if err := r.a.Delete(ctx, rec.ID); !errors.Is(err, archive.ErrBadRecord) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); !errors.Is(err, archive.ErrBadRecord) {
		t.Fatalf("restore: %v", err)
	}
	// A record filed under another id is refused too.
	r.src.Put(archive.RecordPath(rec.ID), r.src.Data(archive.RecordPath(victim.ID)))
	if err := r.a.Delete(ctx, rec.ID); !errors.Is(err, archive.ErrBadRecord) {
		t.Fatalf("misfiled: %v", err)
	}
	for _, id := range []string{"../x", "", "records", strings.ToLower(victim.ID)} {
		if err := r.a.Delete(ctx, id); !errors.Is(err, archive.ErrBadRecord) {
			t.Errorf("id %q: %v", id, err)
		}
		if _, err := r.a.Thumbnail(ctx, id); !errors.Is(err, archive.ErrBadRecord) {
			t.Errorf("thumbnail id %q: %v", id, err)
		}
	}
	if len(r.dst.Log()) != before {
		t.Errorf("the target was touched: %v", r.dst.Log()[before:])
	}
}

func TestDeletePurgesAndIsRetryable(t *testing.T) {
	boom := errors.New("boom")
	for k := 1; ; k++ {
		r, rec := archived(t)
		n := 0
		hook := func(string, string) error {
			if n++; n == k {
				return boom
			}
			return nil
		}
		r.src.Before, r.dst.Before = hook, hook
		err := r.a.Delete(ctx, rec.ID)
		r.src.Before, r.dst.Before = nil, nil
		if err != nil {
			if err := r.a.Delete(ctx, rec.ID); err != nil {
				t.Fatalf("op %d: second try: %v", k, err)
			}
		}
		// A thumbnail whose removal alone failed may stay; nothing else.
		for _, p := range left(r) {
			if p != "drive "+archive.ThumbPath(rec.ID) || err != nil {
				t.Fatalf("op %d: left %v", k, left(r))
			}
		}
		if n < k {
			if k < 5 {
				t.Fatalf("only %d operations", n)
			}
			if !slices.ContainsFunc(r.dst.Log(), func(s string) bool { return s == "purge "+rec.Target.Key }) {
				t.Fatal("the object was not purged")
			}
			return
		}
	}
}

func TestListOrderAndBadRecord(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	if recs, probs, err := r.a.List(ctx); err != nil || len(recs)+len(probs) != 0 {
		t.Fatalf("empty: %v %v %v", recs, probs, err)
	}
	for i, p := range []string{"/old.png", "/new.png", "/middle.png"} {
		r.src.Put(p, r.photo)
		r.src.SetModTime(p, time.Date(2020+[]int{0, 5, 2}[i], 1, 1, 0, 0, 0, 0, time.UTC))
		if _, err := r.a.Archive(ctx, p, archive.ArchiveInput{}); err != nil {
			t.Fatal(err)
		}
	}
	r.src.Put(archive.Dir+"/junk.json", []byte("{"))
	recs, probs, err := r.a.List(ctx)
	if err != nil || len(recs) != 3 || len(probs) != 1 || probs[0].Path != archive.Dir+"/junk.json" {
		t.Fatalf("%d %v %v", len(recs), probs, err)
	}
	var order []string
	for _, rec := range recs {
		order = append(order, rec.OriginalPath)
	}
	if !slices.Equal(order, []string{"/new.png", "/middle.png", "/old.png"}) {
		t.Errorf("order %v", order)
	}
	rc, err := r.a.Thumbnail(ctx, recs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if len(data) < 100 || string(data[:2]) != "\xff\xd8" {
		t.Errorf("thumbnail: %d bytes", len(data))
	}
}
