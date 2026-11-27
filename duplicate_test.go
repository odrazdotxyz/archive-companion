package archive_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	archive "github.com/odrazdotxyz/archive-companion"
)

// twice leaves two copies of the photo in the target, as an archive does that
// stopped after the target's record and was run again. older has no drive
// record; newer is complete.
func twice(t *testing.T) (r *rig, older, newer archive.Record) {
	t.Helper()
	r = newRig(t, archive.VerifyChecksum)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, err := archive.New(archive.Source{FS: r.src, DriveKey: dk}, archive.Target{FS: r.dst, Descriptor: desc},
		archive.Options{Now: func() time.Time { now = now.Add(time.Second); return now }})
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	run := func() archive.Record {
		t.Helper()
		rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	older = run()
	lost(t, r, older.ID)
	r.src.Put(photoPath, r.photo)
	newer = run()
	return r, older, newer
}

// oneOrphan wants the rebuilt ids and a single orphan, the copy of skipped.
func oneOrphan(t *testing.T, r *rig, rebuilt []string, skipped, as string) {
	t.Helper()
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Problems) != 0 || len(got.Rebuilt) != len(rebuilt) || len(got.Orphans) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	for i, id := range rebuilt {
		if got.Rebuilt[i].ID != id {
			t.Errorf("rebuilt %s, want %s", got.Rebuilt[i].ID, id)
		}
	}
	o := got.Orphans[0]
	if o.Key != archive.BucketRecordPath(dk, skipped) || !strings.Contains(o.Reason, as) {
		t.Errorf("orphan %+v", o)
	}
	if recs := r.records(); len(recs) != 1 {
		t.Errorf("%d drive records", len(recs))
	}
}

func TestRescanDoesNotDuplicate(t *testing.T) {
	t.Run("a drive record has it", func(t *testing.T) {
		r, older, newer := twice(t)
		oneOrphan(t, r, nil, older.ID, "already archived as "+newer.ID)
	})
	t.Run("the newest is rebuilt", func(t *testing.T) {
		r, older, newer := twice(t)
		lost(t, r, newer.ID)
		oneOrphan(t, r, []string{newer.ID}, older.ID, "already archived as "+newer.ID)
	})
	t.Run("an unusable newest does not count", func(t *testing.T) {
		r, older, newer := twice(t)
		lost(t, r, newer.ID)
		if err := r.dst.Delete(ctx, newer.Target.Key, false); err != nil {
			t.Fatal(err)
		}
		oneOrphan(t, r, []string{older.ID}, newer.ID, "missing")
	})
	t.Run("an interrupted archive run again", func(t *testing.T) {
		r := newRig(t, archive.VerifyChecksum)
		failed := errors.New("the drive went away")
		r.src.Before = func(op, p string) error {
			if (op == "write" || op == "delete") && strings.HasPrefix(p, archive.Dir) && strings.HasSuffix(p, ".json") {
				return failed
			}
			return nil
		}
		if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); !errors.Is(err, failed) {
			t.Fatal(err)
		}
		r.src.Before = nil
		rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.a.Rescan(ctx)
		if err != nil || len(got.Rebuilt) != 0 || len(got.Orphans) != 1 || !strings.Contains(got.Orphans[0].Reason, "already archived as "+rec.ID) {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// A record whose copy is gone must not hide a good copy of the same file.
func TestRescanRebuildsBesideARecordWithoutItsCopy(t *testing.T) {
	for name, damage := range map[string]func(*rig, archive.Record){
		"gone": func(r *rig, rec archive.Record) { _ = r.dst.Delete(ctx, rec.Target.Key, false) },
		"cut":  func(r *rig, rec archive.Record) { r.dst.Put(rec.Target.Key, []byte("short")) },
	} {
		r, older, newer := twice(t)
		damage(r, newer)
		got, err := r.a.Rescan(ctx)
		if err != nil || len(got.Rebuilt) != 1 || got.Rebuilt[0].ID != older.ID {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
}
