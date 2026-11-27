package archive_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"

	archive "github.com/odrazdotxyz/archive-companion"
	"github.com/odrazdotxyz/archive-companion/internal/fakefs"
)

func TestRescanDoesNotOverwriteARecordWrittenMeanwhile(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	record, thumb := []byte("written meanwhile"), []byte("its thumbnail")
	r.dst.Before = func(op, p string) error {
		if op == "stat" && p == rec.Target.Key { // after Rescan saw no record
			r.src.Put(archive.RecordPath(rec.ID), record)
			r.src.Put(archive.ThumbPath(rec.ID), thumb)
		}
		return nil
	}
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt)+len(got.Orphans)+len(got.Problems) != 0 {
		t.Errorf("%+v %v", got, err)
	}
	if !bytes.Equal(r.src.Data(archive.RecordPath(rec.ID)), record) || !bytes.Equal(r.src.Data(archive.ThumbPath(rec.ID)), thumb) {
		t.Error("overwritten")
	}
}

func TestRescanKeepsALeftoverThumbnail(t *testing.T) {
	r, rec := archived(t)
	thumb := r.src.Data(archive.ThumbPath(rec.ID))
	if err := r.src.Delete(ctx, archive.RecordPath(rec.ID), false); err != nil {
		t.Fatal(err)
	}
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt) != 1 || len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if !bytes.Equal(r.src.Data(archive.ThumbPath(rec.ID)), thumb) {
		t.Error("the thumbnail was replaced")
	}
	if got.Rebuilt[0].Thumb != rec.Thumb {
		t.Errorf("the record describes another thumbnail: %+v, want %+v", got.Rebuilt[0].Thumb, rec.Thumb)
	}
}

// A rebuild that wrote its placeholder and then failed leaves that placeholder:
// the next one must still call it one.
func TestRescanKnowsItsLeftoverPlaceholder(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	r.src.Before = func(op, p string) error {
		if op == "write" && p == archive.RecordPath(rec.ID) {
			return errors.New("the write failed")
		}
		return nil
	}
	if got, err := r.a.Rescan(ctx); err != nil || len(got.Problems) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	r.src.Before = nil
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt) != 1 || !got.Rebuilt[0].Thumb.Placeholder {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRescanTakesTurnsWithTheFile(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	var other error
	r.src.Before = func(op, p string) error {
		if op == "write" && p == archive.RecordPath(rec.ID) {
			short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			_, other = r.a.Archive(short, photoPath, archive.ArchiveInput{})
		}
		return nil
	}
	if _, err := r.a.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(other, context.DeadlineExceeded) {
		t.Errorf("an operation on the file did not wait: %v", other)
	}
}

func TestReservedFolderAnyCase(t *testing.T) {
	for _, p := range []string{"/.ODRAZ/x.jpg", "/.Odraz/archive/x.jpg", "/.oDrAz"} {
		r := newRig(t, archive.VerifyChecksum)
		r.src.Put(p, pngBytes(10, 10))
		r.src.SetMime(p, "image/jpeg")
		if _, err := r.a.Archive(ctx, p, archive.ArchiveInput{}); !errors.Is(err, odraz.ErrInvalidPath) || !r.src.Has(p) {
			t.Errorf("archive %s: %v", p, err)
		}
		rec := sample(archive.NewID(time.Now()))
		rec.OriginalPath = p
		rec.Target.Key = archive.ObjectKey(dk, rec.ID, p[strings.LastIndex(p, "/")+1:])
		data, err := rec.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := archive.ParseRecord(data, dk); !errors.Is(err, archive.ErrBadRecord) {
			t.Errorf("parse %s: %v", p, err)
		}
	}
	// A folder that only starts the same is the user's.
	r := newRig(t, archive.VerifyChecksum)
	r.src.Put("/.odrazed/x.png", r.photo)
	if _, err := r.a.Archive(ctx, "/.odrazed/x.png", archive.ArchiveInput{}); err != nil {
		t.Error(err)
	}
}

func TestDescriptorRefusesQueryCredentials(t *testing.T) {
	for _, endpoint := range []string{
		"https://s3.example.org?X-Amz-Credential=secret",
		"https://s3.example.org/#secret",
		"s3.example.org?token=secret",
		"https://key:secret@s3.example.org",
		"https://s3.example.org?",
	} {
		bad := archive.TargetDescriptor{Endpoint: endpoint, Bucket: "b"}
		refused := func(what string, err error) {
			t.Helper()
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Errorf("%s %s: %v", what, endpoint, err)
			}
		}
		fs := fakefs.New()
		refused("set", archive.SetTarget(ctx, fs, bad))
		if fs.Has(manifest) {
			t.Errorf("set %s: written", endpoint)
		}
		_, err := archive.New(archive.Source{FS: fs, DriveKey: dk}, archive.Target{FS: fakefs.New(), Descriptor: bad}, archive.Options{})
		refused("new", err)
		fs.Put(manifest, []byte(`{"version":1,"sections":{"archive":{"endpoint":"`+endpoint+`","bucket":"b"}}}`))
		_, ok, err := archive.ReadTarget(ctx, fs)
		refused("read", err)
		if ok {
			t.Errorf("read %s: accepted", endpoint)
		}
		// Nor is one repeated from a record on the drive.
		r, rec := archived(t)
		rec.Target.Endpoint = endpoint
		data, err := rec.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		r.src.Put(archive.RecordPath(rec.ID), data)
		refused("delete", r.a.Delete(ctx, rec.ID))
		if got := r.dst.Files(); !slices.Contains(got, rec.Target.Key) {
			t.Errorf("delete %s: the copy went", endpoint)
		}
	}
}

func TestRescanReportsADamagedDriveRecord(t *testing.T) {
	r, rec := archived(t)
	damaged := []byte(`{"version":1,`)
	r.src.Put(archive.RecordPath(rec.ID), damaged)
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt)+len(got.Orphans) != 0 || len(got.Problems) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if p := got.Problems[0]; p.Path != archive.RecordPath(rec.ID) || !errors.Is(p.Err, archive.ErrBadRecord) {
		t.Errorf("problem %+v", p)
	}
	if !bytes.Equal(r.src.Data(archive.RecordPath(rec.ID)), damaged) {
		t.Error("the damaged record was overwritten")
	}
}

// The lock is on the path the record named: it must not name another once the
// lock is held.
func TestRecordThatMovedWhileWaitingIsRefused(t *testing.T) {
	for name, act := range map[string]func(*rig, string) error{
		"delete": func(r *rig, id string) error { return r.a.Delete(ctx, id) },
		"restore": func(r *rig, id string) error {
			_, err := r.a.Restore(ctx, id, archive.RestoreInput{})
			return err
		},
	} {
		r, rec := archived(t)
		moved := rec
		moved.OriginalPath = "/elsewhere/IMG 1.png"
		data, err := moved.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		reads := 0
		r.src.Before = func(op, p string) error {
			if op == "read" && p == archive.RecordPath(rec.ID) {
				if reads++; reads == 2 { // the read made under the lock
					r.src.Put(p, data)
				}
			}
			return nil
		}
		if err := act(r, rec.ID); !errors.Is(err, archive.ErrBadRecord) {
			t.Errorf("%s: %v", name, err)
		}
		if !r.dst.Has(rec.Target.Key) || !r.src.Has(archive.RecordPath(rec.ID)) || r.src.Has(moved.OriginalPath) {
			t.Errorf("%s: something was changed: %v %v", name, r.src.Files(), r.dst.Files())
		}
	}
}
