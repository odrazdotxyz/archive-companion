package archive_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"

	archive "github.com/odrazdotxyz/archive-companion"
	"github.com/odrazdotxyz/archive-companion/internal/fakefs"
)

// lost drops the drive record and thumbnail, as when a server is rebuilt.
func lost(t *testing.T, r *rig, id string) {
	t.Helper()
	for _, p := range []string{archive.RecordPath(id), archive.ThumbPath(id)} {
		if err := r.src.Delete(ctx, p, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRescanRebuilds(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt) != 1 || len(got.Orphans)+len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	recs, _, _ := r.a.List(ctx)
	if len(recs) != 1 || !recs[0].Rebuilt || !recs[0].Thumb.Placeholder || recs[0].SHA256 != rec.SHA256 || recs[0].OriginalPath != photoPath || !r.src.Has(archive.ThumbPath(rec.ID)) {
		t.Fatalf("%+v", recs)
	}
	// And the rebuilt item restores.
	if _, err := r.a.Restore(ctx, rec.ID, archive.RestoreInput{}); err != nil || !bytes.Equal(r.src.Data(photoPath), r.photo) {
		t.Fatal(err)
	}
}

func TestRescanOnAnEmptyTarget(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt)+len(got.Orphans)+len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func onlyOrphan(t *testing.T, r *rig, want string) {
	t.Helper()
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt) != 0 || len(got.Orphans) != 1 || !strings.Contains(got.Orphans[0].Reason, want) {
		t.Fatalf("want an orphan (%s): %+v %v", want, got, err)
	}
	for _, p := range r.src.Files() {
		if strings.HasPrefix(p, archive.Dir) {
			t.Fatalf("written to the drive: %s", p)
		}
	}
}

func TestRescanSkipsWhenOriginalExists(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	r.src.Put(photoPath, r.photo)
	onlyOrphan(t, r, "still on the drive")
}

func TestRescanReportsObjectWithoutRecord(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	if err := r.dst.Delete(ctx, archive.BucketRecordPath(dk, rec.ID), false); err != nil {
		t.Fatal(err)
	}
	onlyOrphan(t, r, "no record")
}

func TestRescanReportsMissingOrWrongObject(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	r.dst.Put(rec.Target.Key, []byte("short"))
	onlyOrphan(t, r, "size")
	if err := r.dst.Delete(ctx, rec.Target.Key, false); err != nil {
		t.Fatal(err)
	}
	onlyOrphan(t, r, "missing")
}

func TestRescanRejectsHostileRecord(t *testing.T) {
	for name, change := range map[string]func(*archive.Record){
		"escaping path": func(rec *archive.Record) { rec.OriginalPath = "/../x.png" },
		"reserved path": func(rec *archive.Record) { rec.OriginalPath = "/.odraz/x.png" },
		"other key":     func(rec *archive.Record) { rec.Target.Key = "/odraz/" + dk + "/records/x" },
		"other target":  func(rec *archive.Record) { rec.Target.Bucket = "elsewhere" },
	} {
		r, rec := archived(t)
		lost(t, r, rec.ID)
		bad := rec
		change(&bad)
		data, _ := bad.Marshal()
		r.dst.Put(archive.BucketRecordPath(dk, rec.ID), data)
		got, err := r.a.Rescan(ctx)
		if err != nil || len(got.Rebuilt) != 0 || len(got.Orphans) != 1 {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
}

func TestRescanKeepsExistingRecords(t *testing.T) {
	r, rec := archived(t)
	before := r.src.Data(archive.RecordPath(rec.ID))
	got, err := r.a.Rescan(ctx)
	if err != nil || len(got.Rebuilt)+len(got.Orphans) != 0 || !bytes.Equal(before, r.src.Data(archive.RecordPath(rec.ID))) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRescanDownloadsNoOriginal(t *testing.T) {
	r, rec := archived(t)
	lost(t, r, rec.ID)
	if _, err := r.a.Rescan(ctx); err != nil || r.dst.Reads(rec.Target.Key) != 0 {
		t.Fatalf("%v, reads %d", err, r.dst.Reads(rec.Target.Key))
	}
}

const manifest = "/.odraz/manifest.json"

func TestManifestKeepsOtherSections(t *testing.T) {
	fs := fakefs.New()
	if _, ok, err := archive.ReadTarget(ctx, fs); ok || err != nil {
		t.Fatal(ok, err)
	}
	fs.Put(manifest, []byte(`{"version":1,"later":true,"sections":{"photos":{"folder":"/Pictures","hideInFiles":true,"new":7},"archive":{"bucket":"old","note":"kept"}}}`))
	want := archive.TargetDescriptor{Endpoint: "s3.example.org", Bucket: "b", Prefix: "p/"}
	if err := archive.SetTarget(ctx, fs, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := archive.ReadTarget(ctx, fs)
	if err != nil || !ok || got != want {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	text := strings.Join(strings.Fields(string(fs.Data(manifest))), "")
	for _, kept := range []string{`"later":true`, `"hideInFiles":true`, `"new":7`, `"note":"kept"`} {
		if !strings.Contains(text, kept) {
			t.Errorf("%s lost: %s", kept, text)
		}
	}
	if err := archive.RemoveTarget(ctx, fs); err != nil {
		t.Fatal(err)
	}
	text = strings.Join(strings.Fields(string(fs.Data(manifest))), "")
	if strings.Contains(text, "archive") || !strings.Contains(text, `"hideInFiles":true`) {
		t.Errorf("after removal: %s", text)
	}
}

func TestSetTargetCreatesTheManifest(t *testing.T) {
	fs := fakefs.New()
	if err := archive.SetTarget(ctx, fs, desc); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := archive.ReadTarget(ctx, fs); err != nil || !ok || got != desc {
		t.Fatal(got, ok, err)
	}
}

func TestManifestRefusesUnreadable(t *testing.T) {
	for name, content := range map[string]string{
		"newer": `{"version":2,"sections":{}}`, "broken": `{`, "no version": `{"sections":{}}`,
		"big": `{"version":1,"sections":{},"x":"` + strings.Repeat("y", 70<<10) + `"}`,
	} {
		fs := fakefs.New()
		fs.Put(manifest, []byte(content))
		for op, err := range map[string]error{"set": archive.SetTarget(ctx, fs, desc), "remove": archive.RemoveTarget(ctx, fs)} {
			if !errors.Is(err, archive.ErrManifest) {
				t.Errorf("%s %s: %v", name, op, err)
			}
		}
		if string(fs.Data(manifest)) != content {
			t.Errorf("%s: overwritten", name)
		}
		if _, ok, err := archive.ReadTarget(ctx, fs); ok || !errors.Is(err, archive.ErrManifest) {
			t.Errorf("%s: read %v %v", name, ok, err)
		}
	}
}

func TestRemoveLastSectionCleansUp(t *testing.T) {
	fs := fakefs.New()
	if err := archive.SetTarget(ctx, fs, desc); err != nil {
		t.Fatal(err)
	}
	if err := archive.RemoveTarget(ctx, fs); err != nil {
		t.Fatal(err)
	}
	if fs.Has(manifest) || fs.Has("/.odraz") {
		t.Fatalf("left: %v", fs.Files())
	}
	if err := archive.RemoveTarget(ctx, fs); err != nil {
		t.Fatalf("again: %v", err)
	}
	// With records in /.odraz the folder stays.
	if err := archive.SetTarget(ctx, fs, desc); err != nil {
		t.Fatal(err)
	}
	fs.Put(archive.Dir+"/x.json", []byte("{}"))
	if err := archive.RemoveTarget(ctx, fs); err != nil || fs.Has(manifest) || !fs.Has(archive.Dir+"/x.json") {
		t.Fatal(err, fs.Files())
	}
}

func TestNoCredentialsInADescriptor(t *testing.T) {
	bad := archive.TargetDescriptor{Endpoint: "https://key:secret@s3.example.org", Bucket: "b"}
	fs := fakefs.New()
	err := archive.SetTarget(ctx, fs, bad)
	if !errors.Is(err, odraz.ErrInvalidPath) || strings.Contains(err.Error(), "secret") || fs.Has(manifest) {
		t.Errorf("set: %v", err)
	}
	_, err = archive.New(archive.Source{FS: fs, DriveKey: dk}, archive.Target{FS: fakefs.New(), Descriptor: bad}, archive.Options{})
	if !errors.Is(err, odraz.ErrInvalidPath) || strings.Contains(err.Error(), "secret") {
		t.Errorf("new: %v", err)
	}
}
