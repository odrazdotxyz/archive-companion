package archive_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/odrazdotxyz/odraz-go-sdk/files"

	archive "github.com/odrazdotxyz/archive-companion"
)

// rewriteAtDelete changes the photo after the last look at it, as the
// conditional delete reaches the server.
func rewriteAtDelete(r *rig, content []byte) {
	r.src.Before = func(op, p string) error {
		if op == "deletematch" && p == photoPath {
			r.src.Put(photoPath, content)
		}
		return nil
	}
}

func TestChangeAtDeleteTimeKeepsTheFile(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	edited := pngBytes(30, 30)
	rewriteAtDelete(r, edited)
	_, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if !errors.Is(err, archive.ErrSourceChanged) {
		t.Fatalf("got %v", err)
	}
	if !bytes.Equal(r.src.Data(photoPath), edited) {
		t.Fatal("the edited file was not kept")
	}
	if got := r.src.Files(); !slices.Equal(got, []string{photoPath}) {
		t.Errorf("left on the drive: %v", got)
	}
	if got := r.dst.Files(); len(got) != 0 {
		t.Errorf("left in the target: %v", got)
	}
}

func TestResumeConditionalDelete(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	failed := errors.New("the delete failed")
	r.src.Before = func(op, p string) error {
		if op == "deletematch" && p == photoPath {
			return failed
		}
		return nil
	}
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); !errors.Is(err, failed) {
		t.Fatalf("first run: %v", err)
	}
	first := r.records()
	if len(first) != 1 || !r.src.Has(photoPath) {
		t.Fatalf("first run left %v", r.src.Files())
	}

	// The file is rewritten as the resumed run deletes it: it stays.
	edited := pngBytes(30, 30)
	rewriteAtDelete(r, edited)
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); !errors.Is(err, archive.ErrSourceChanged) {
		t.Fatalf("resume: %v", err)
	}
	if !bytes.Equal(r.src.Data(photoPath), edited) {
		t.Fatal("the edited file was not kept")
	}
	if got := r.records(); len(got) != 1 || got[0].ID != first[0].ID {
		t.Fatalf("the earlier record must stay: %+v", got)
	}
	r.invariants("after the refused delete")

	// Unchanged, the resumed run deletes it conditionally.
	r2 := newRig(t, archive.VerifyChecksum)
	r2.src.Before = func(op, p string) error {
		if op == "deletematch" && p == photoPath {
			return failed
		}
		return nil
	}
	_, _ = r2.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	r2.src.Before = nil
	before := len(r2.src.Log())
	rec, err := r2.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || r2.src.Has(photoPath) || len(r2.records()) != 1 || r2.records()[0].ID != rec.ID {
		t.Fatalf("%v, left %v", err, r2.src.Files())
	}
	if log := r2.src.Log()[before:]; !slices.Contains(log, "deletematch "+photoPath) || slices.Contains(log, "delete "+photoPath) {
		t.Errorf("not deleted conditionally: %v", log)
	}
}

// plain is a drive that has no conditional delete at all.
type plain struct{ files.FS }

// untagged is a drive that gives no ETags.
type untagged struct{ files.FS }

func (u untagged) Stat(ctx context.Context, p string) (files.Entry, error) {
	e, err := u.FS.Stat(ctx, p)
	e.ETag = ""
	return e, err
}

func (u untagged) DeleteIfMatch(context.Context, string, string) error {
	return errors.New("called without an ETag to compare")
}

func TestDriveWithoutConditionalDelete(t *testing.T) {
	for name, wrap := range map[string]func(*rig) files.FS{
		"no method":   func(r *rig) files.FS { return plain{r.src} },
		"unsupported": func(r *rig) files.FS { r.src.NoConditionalDelete = true; return r.src },
		"no ETag":     func(r *rig) files.FS { return untagged{r.src} },
	} {
		r := newRig(t, archive.VerifyChecksum)
		a, err := archive.New(archive.Source{FS: wrap(r), DriveKey: dk}, archive.Target{FS: r.dst, Descriptor: desc}, archive.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Archive(ctx, photoPath, archive.ArchiveInput{}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.src.Has(photoPath) || !slices.Contains(r.src.Log(), "delete "+photoPath) {
			t.Errorf("%s: not deleted: %v", name, r.src.Log())
		}
	}
}
