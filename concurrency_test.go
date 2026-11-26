package archive_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	archive "github.com/odrazdotxyz/archive-companion"
)

// somewhere reports whether the photo still exists, on the drive or in the target.
func somewhere(r *rig) bool {
	for _, fs := range []interface {
		Files() []string
		Data(string) []byte
	}{r.src, r.dst} {
		for _, p := range fs.Files() {
			if bytes.Equal(fs.Data(p), r.photo) {
				return true
			}
		}
	}
	return false
}

// A file identical to an archived item is back at its path; restoring the item
// and archiving the file at the same time must not lose both.
func TestArchiveAndRestoreOfTheSameFileCannotLoseIt(t *testing.T) {
	r, rec := archived(t)
	r.src.Put(photoPath, r.photo)
	// A second Archive value, as another session of the same application has.
	other, err := archive.New(archive.Source{FS: r.src, DriveKey: dk}, archive.Target{FS: r.dst, Descriptor: desc}, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	started := false
	r.dst.Before = func(op, p string) error {
		if op == "purge" && p == rec.Target.Key && !started {
			started = true
			go func() { _, err := other.Archive(ctx, photoPath, archive.ArchiveInput{}); done <- err }()
			select { // give it the chance to run into the restore
			case err := <-done:
				done <- err
			case <-time.After(300 * time.Millisecond):
			}
		}
		return nil
	}
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !somewhere(r) {
		t.Fatalf("the photo is nowhere: drive %v, target %v", r.src.Files(), r.dst.Files())
	}
}

// From another device (no shared lock): the record goes while an earlier run is
// being finished.
func TestResumeLeavesTheFileWhenTheRecordIsGone(t *testing.T) {
	r, rec := archived(t)
	r.src.Put(photoPath, r.photo)
	r.dst.Before = func(op, p string) error {
		if op == "verify" && p == rec.Target.Key { // another device removes the record now
			r.dst.Before = nil
			if err := r.src.Delete(ctx, archive.RecordPath(rec.ID), false); err != nil {
				t.Error(err)
			}
		}
		return nil
	}
	_, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err == nil || !r.src.Has(photoPath) {
		t.Fatalf("err %v, file kept %v", err, r.src.Has(photoPath))
	}
}

// From another device: the file a restore found in place goes before the copy
// is removed.
func TestRestoreKeepsTheCopyWhenTheAdoptedFileGoes(t *testing.T) {
	r, rec := archived(t)
	r.src.Put(photoPath, r.photo)
	reads := 0
	r.src.Before = func(op, p string) error {
		if op == "stat" && p == photoPath {
			if reads++; reads == 2 { // after it was found and hashed
				r.src.Before = nil
				if err := r.src.Delete(ctx, photoPath, false); err != nil {
					t.Error(err)
				}
			}
		}
		return nil
	}
	_, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{})
	if err == nil || r.dst.Data(rec.Target.Key) == nil {
		t.Fatalf("err %v, copy kept %v", err, r.dst.Data(rec.Target.Key) != nil)
	}
}

func TestDriveKey(t *testing.T) {
	a := archive.DriveKey("https://cloud.example.org", "alice", "personal")
	if a != archive.DriveKey("HTTPS://Cloud.Example.org/", "alice", "personal") {
		t.Error("the same server written differently gives another key")
	}
	seen := map[string]bool{a: true}
	for _, k := range []string{
		archive.DriveKey("https://cloud.example.org", "bob", "personal"),
		archive.DriveKey("https://other.example.org", "alice", "personal"),
		archive.DriveKey("https://cloud.example.org", "alice", "team"),
		archive.DriveKey("https://cloud.example.org", "alicepersonal", ""),
		archive.DriveKey("https://cloud.example.org/Cloud", "alice", "personal"),
		archive.DriveKey("https://cloud.example.org/cloud", "alice", "personal"),
	} {
		if seen[k] {
			t.Errorf("collision: %s", k)
		}
		seen[k] = true
	}
	if _, err := archive.New(archive.Source{FS: newRig(t, archive.VerifyChecksum).src, DriveKey: a}, archive.Target{FS: newRig(t, archive.VerifyChecksum).dst, Descriptor: desc}, archive.Options{}); err != nil || strings.Contains(a, "alice") {
		t.Errorf("key %q: %v", a, err)
	}
}
