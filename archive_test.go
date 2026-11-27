package archive_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/files"

	archive "github.com/odrazdotxyz/archive-companion"
	"github.com/odrazdotxyz/archive-companion/internal/fakefs"
)

var ctx = context.Background()

func pngBytes(w, h int) []byte {
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i % 251)
	}
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type rig struct {
	t        *testing.T
	src, dst *fakefs.FS
	a        *archive.Archive
	photo    []byte
}

const photoPath = "/Fotos/Köln/IMG 1.png"

var desc = archive.TargetDescriptor{Endpoint: "s3.example.org", Bucket: "b", Prefix: ""}

func newRig(t *testing.T, mode archive.VerifyMode) *rig {
	t.Helper()
	r := &rig{t: t, src: fakefs.New(), dst: fakefs.New(), photo: pngBytes(800, 400)}
	r.src.Put(photoPath, r.photo)
	a, err := archive.New(archive.Source{FS: r.src, DriveKey: dk}, archive.Target{FS: r.dst, Descriptor: desc}, archive.Options{Verify: mode})
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	return r
}

// records parses every drive record.
func (r *rig) records() (out []archive.Record) {
	r.t.Helper()
	for _, p := range r.src.Files() {
		if strings.HasPrefix(p, archive.Dir+"/") && strings.HasSuffix(p, ".json") {
			rec, err := archive.ParseRecord(r.src.Data(p), dk)
			if err != nil {
				r.t.Fatalf("%s: %v", p, err)
			}
			out = append(out, rec)
		}
	}
	return out
}

// invariants: no drive record without a verified object and bucket record, and
// the original is there unless a record is complete.
func (r *rig) invariants(when string) {
	r.t.Helper()
	recs := r.records()
	for _, rec := range recs {
		if got := r.dst.Data(rec.Target.Key); sum(got) != rec.SHA256 {
			r.t.Fatalf("%s: record %s points at an object that is not the file", when, rec.ID)
		}
		if r.dst.Data(archive.BucketRecordPath(dk, rec.ID)) == nil {
			r.t.Fatalf("%s: no bucket record for %s", when, rec.ID)
		}
		if r.src.Data(archive.ThumbPath(rec.ID)) == nil {
			r.t.Fatalf("%s: no thumbnail for %s", when, rec.ID)
		}
	}
	if !r.src.Has(photoPath) && len(recs) == 0 {
		r.t.Fatalf("%s: the original is gone and there is no record", when)
	}
}

func TestArchiveMovesFileAndWritesDriveRecordLast(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	var stages []archive.Stage
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{Progress: func(s archive.Stage, _, _ int64) {
		if len(stages) == 0 || stages[len(stages)-1] != s {
			stages = append(stages, s)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if r.src.Has(photoPath) {
		t.Error("the original is still there")
	}
	if !bytes.Equal(r.dst.Data(rec.Target.Key), r.photo) || rec.Target.Key != archive.ObjectKey(dk, rec.ID, "IMG 1.png") {
		t.Errorf("object %q wrong", rec.Target.Key)
	}
	recs := r.records()
	if len(recs) != 1 || recs[0].ID != rec.ID || rec.SHA256 != sum(r.photo) || rec.Size != int64(len(r.photo)) ||
		rec.OriginalPath != photoPath || rec.Width != 800 || rec.Height != 400 || rec.Target.Bucket != "b" || rec.Thumb.W != 512 || rec.Thumb.Placeholder {
		t.Errorf("record %+v", rec)
	}
	if !bytes.Equal(r.dst.Data(archive.BucketRecordPath(dk, rec.ID)), r.src.Data(archive.RecordPath(rec.ID))) {
		t.Error("the bucket record differs from the drive record")
	}
	// Order across both: object, its verification, bucket record, thumbnail, drive record, delete.
	var order []string
	note := func(fs string) func(op, p string) error {
		return func(op, p string) error {
			if op == "write" || op == "verify" || op == "deletematch" {
				order = append(order, fs+" "+op+" "+p)
			}
			return nil
		}
	}
	r2 := newRig(t, archive.VerifyChecksum)
	r2.src.Before, r2.dst.Before = note("src"), note("dst")
	rec2, err := r2.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	brec := archive.BucketRecordPath(dk, rec2.ID)
	want := []string{
		"dst write " + rec2.Target.Key, "dst verify " + rec2.Target.Key, "dst write " + brec, "dst verify " + brec,
		"src write " + archive.ThumbPath(rec2.ID), "src write " + archive.RecordPath(rec2.ID), "src deletematch " + photoPath,
	}
	if !slices.Equal(order, want) {
		t.Errorf("order:\n%s", strings.Join(order, "\n"))
	}
	if !slices.Equal(stages, []archive.Stage{archive.Uploading, archive.Verifying, archive.Saving, archive.Removing}) {
		t.Errorf("stages %v", stages)
	}
}

func TestSourceReadOnce(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); err != nil {
		t.Fatal(err)
	}
	if n := r.src.Reads(photoPath); n != 1 {
		t.Fatalf("read %d times", n)
	}
}

func objects(r *rig) (out []string) {
	for _, p := range r.dst.Files() {
		out = append(out, p)
	}
	return out
}

func TestUnverifiedKeepsOriginal(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.dst.DamageWrites = "IMG"
	_, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if !errors.Is(err, archive.ErrNotVerified) {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(r.src.Data(photoPath), r.photo) || len(r.records()) != 0 || len(objects(r)) != 0 {
		t.Fatalf("left: source %v, records %d, objects %v", r.src.Has(photoPath), len(r.records()), objects(r))
	}
}

func TestDamagedBucketRecordKeepsOriginal(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.dst.DamageWrites = "/records/"
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); !errors.Is(err, archive.ErrNotVerified) {
		t.Fatalf("err = %v", err)
	}
	if !r.src.Has(photoPath) || len(r.records()) != 0 || len(objects(r)) != 0 {
		t.Fatalf("objects %v", objects(r))
	}
}

func TestShortAndLongSourceStreamKeepOriginal(t *testing.T) {
	for _, long := range []bool{false, true} {
		r := newRig(t, archive.VerifyChecksum)
		if long {
			r.src.Long[photoPath] = true
		} else {
			r.src.Short[photoPath] = true
		}
		if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); err == nil {
			t.Fatalf("long=%v: success", long)
		}
		if !r.src.Has(photoPath) || len(r.records()) != 0 || len(objects(r)) != 0 {
			t.Fatalf("long=%v: objects %v", long, objects(r))
		}
	}
}

func TestChecksumUnsupportedFallsBackToDownload(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.dst.NoChecksum = true
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || r.dst.Reads(rec.Target.Key) != 1 {
		t.Fatalf("%v, reads %d", err, r.dst.Reads(rec.Target.Key))
	}
	r = newRig(t, archive.VerifyChecksum)
	r.dst.NoChecksum, r.dst.DamageWrites = true, "IMG"
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); !errors.Is(err, archive.ErrNotVerified) || !r.src.Has(photoPath) {
		t.Fatalf("damaged, read back: %v", err)
	}
}

func TestChecksumDownloadsNothing(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || r.dst.Reads(rec.Target.Key) != 0 {
		t.Fatalf("%v, reads %d", err, r.dst.Reads(rec.Target.Key))
	}
}

func TestVerifyDownloadAlwaysReads(t *testing.T) {
	r := newRig(t, archive.VerifyDownload)
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || r.dst.Reads(rec.Target.Key) != 1 {
		t.Fatalf("%v, reads %d", err, r.dst.Reads(rec.Target.Key))
	}
}

func TestSourceChangedKeepsOriginal(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	newer := pngBytes(30, 30)
	r.dst.Before = func(op, p string) error { // rewritten while the upload runs
		if op == "verify" && strings.Contains(p, "IMG") {
			r.src.Put(photoPath, newer)
		}
		return nil
	}
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); !errors.Is(err, archive.ErrSourceChanged) {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(r.src.Data(photoPath), newer) || len(r.records()) != 0 || len(objects(r)) != 0 {
		t.Fatalf("source kept %v, records %d, objects %v", r.src.Has(photoPath), len(r.records()), objects(r))
	}
	for _, p := range r.src.Files() {
		if strings.HasPrefix(p, archive.Dir) {
			t.Errorf("left on the drive: %s", p)
		}
	}
}

// Someone else removed the file meanwhile: the archive copy is now the only one.
func TestSourceVanishedKeepsTheArchive(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.dst.Before = func(op, p string) error {
		if op == "verify" && strings.Contains(p, "IMG") {
			r.src.Before = nil
			_ = r.src.Delete(ctx, photoPath, false)
		}
		return nil
	}
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || len(r.records()) != 1 || r.dst.Data(rec.Target.Key) == nil {
		t.Fatalf("%v, records %d", err, len(r.records()))
	}
}

func TestFailureAfterEachStepThenRerunFinishes(t *testing.T) {
	boom := errors.New("boom")
	for k := 1; ; k++ {
		r := newRig(t, archive.VerifyChecksum)
		n, failed := 0, ""
		hook := func(op, p string) error {
			if n++; n == k {
				failed = op + " " + p
				return boom
			}
			return nil
		}
		r.src.Before, r.dst.Before = hook, hook
		_, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
		r.src.Before, r.dst.Before = nil, nil
		if n < k {
			if k < 10 {
				t.Fatalf("only %d operations", n)
			}
			break // every operation has had its turn
		}
		when := "failing #" + failed
		r.invariants(when)
		if err != nil {
			if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); err != nil {
				t.Fatalf("%s: second run: %v", when, err)
			}
		}
		r.invariants(when + ", after the second run")
		if r.src.Has(photoPath) || len(r.records()) != 1 {
			t.Fatalf("%s: original %v, records %d", when, r.src.Has(photoPath), len(r.records()))
		}
	}
}

// The drive says the record write failed but kept it: the object must stay.
func TestLostResponseOnDriveRecordKeepsObject(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.src.Before = func(op, p string) error {
		if op == "delete" && strings.HasSuffix(p, ".json") {
			return errors.New("offline")
		}
		if op == "write" && strings.HasSuffix(p, ".json") {
			r.src.Put(p, r.dst.Data(archive.BucketRecordPath(dk, strings.TrimSuffix(p[len(archive.Dir)+1:], ".json"))))
			return errors.New("connection reset")
		}
		return nil
	}
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); err == nil {
		t.Fatal("success")
	}
	r.invariants("lost response")
	if !r.src.Has(photoPath) {
		t.Fatal("original gone")
	}
}

func TestResumeDeletesOnlyTheSameContent(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.src.Before = func(op, p string) error {
		if op == "deletematch" && p == photoPath {
			return errors.New("offline")
		}
		return nil
	}
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); err == nil {
		t.Fatal("success")
	}
	r.src.Before = nil
	first := r.records()[0]
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || rec.ID != first.ID || r.src.Has(photoPath) || len(r.records()) != 1 {
		t.Fatalf("resume: %v, id %s/%s", err, rec.ID, first.ID)
	}
}

func TestResumeDoesNotDeleteChangedFile(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); err != nil {
		t.Fatal(err)
	}
	newer := pngBytes(20, 20)
	r.src.Put(photoPath, newer)
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || rec.SHA256 != sum(newer) || len(r.records()) != 2 {
		t.Fatalf("%v, records %d", err, len(r.records()))
	}
	if !bytes.Equal(r.dst.Data(rec.Target.Key), newer) {
		t.Fatal("the new content is not archived")
	}
}

func TestResumeWithDamagedCopyArchivesAgain(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	first, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil {
		t.Fatal(err)
	}
	r.dst.Corrupt(first.Target.Key)
	r.src.Put(photoPath, r.photo)
	rec, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{})
	if err != nil || rec.ID == first.ID || !bytes.Equal(r.dst.Data(rec.Target.Key), r.photo) {
		t.Fatalf("%v", err)
	}
}

func TestCancelledContext(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	c, cancel := context.WithCancel(ctx)
	r.dst.Before = func(op, p string) error {
		if op == "write" {
			cancel()
		}
		return nil
	}
	if _, err := r.a.Archive(c, photoPath, archive.ArchiveInput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !r.src.Has(photoPath) || len(r.records()) != 0 || len(objects(r)) != 0 {
		t.Fatalf("objects %v", objects(r))
	}
}

func TestNothingBuffersAWholeVideo(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	const size = 200 << 20
	r.src.PutGenerated("/v.mp4", size)
	r.dst.Discard = true
	runtime.GC()
	var base, ms runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	last := time.Now()
	rec, err := r.a.Archive(ctx, "/v.mp4", archive.ArchiveInput{Progress: func(archive.Stage, int64, int64) {}})
	runtime.ReadMemStats(&ms)
	_ = last
	if ms.HeapSys > base.HeapSys {
		peak = ms.HeapSys - base.HeapSys
	}
	if err != nil || rec.Size != size || !rec.Thumb.Placeholder {
		t.Fatalf("%v %+v", err, rec)
	}
	if peak > 64<<20 {
		t.Fatalf("the heap grew by %d MiB for a 200 MiB file", peak>>20)
	}
}

func TestNotMedia(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.src.Put("/a.txt", []byte("x"))
	r.src.Put("/.odraz/archive/photos/x.jpg", []byte("x"))
	for p, want := range map[string]error{"/a.txt": archive.ErrNotMedia, "/Fotos": archive.ErrNotMedia, "/.odraz/archive/photos/x.jpg": odraz.ErrInvalidPath, "/": odraz.ErrInvalidPath, "/gone.jpg": odraz.ErrNotFound} {
		if _, err := r.a.Archive(ctx, p, archive.ArchiveInput{}); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", p, err, want)
		}
	}
	if len(objects(r)) != 0 {
		t.Errorf("objects %v", objects(r))
	}
}

func TestReadOnlyIsRefusedBeforeAnyWork(t *testing.T) {
	for _, which := range []string{"source", "target"} {
		r := newRig(t, archive.VerifyChecksum)
		r.src.ReadOnly, r.dst.ReadOnly = which == "source", which == "target"
		if _, err := r.a.Archive(ctx, photoPath, archive.ArchiveInput{}); !errors.Is(err, odraz.ErrPermission) {
			t.Errorf("%s: %v", which, err)
		}
		if len(r.src.Log())+len(r.dst.Log()) != 0 {
			t.Errorf("%s: operations ran", which)
		}
	}
}

func TestCallerThumbnailUsed(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	r.src.Put("/v.mp4", []byte("not really a video"))
	rec, err := r.a.Archive(ctx, "/v.mp4", archive.ArchiveInput{Thumbnail: pngBytes(100, 50)})
	if err != nil || rec.Thumb.Placeholder || rec.Thumb.W != 100 || rec.Thumb.H != 50 {
		t.Fatalf("%v %+v", err, rec.Thumb)
	}
}

func TestThumbnailSources(t *testing.T) {
	// The server's preview, for a file Go cannot decode.
	r := newRig(t, archive.VerifyChecksum)
	r.src.PreviewImage = pngBytes(64, 32)
	r.src.Put("/h.heic", []byte("heic bytes"))
	rec, err := r.a.Archive(ctx, "/h.heic", archive.ArchiveInput{})
	if err != nil || rec.Thumb.Placeholder || rec.Thumb.W != 64 {
		t.Fatalf("preview: %v %+v", err, rec.Thumb)
	}
	// Nothing to make one from: a placeholder, and the archive still succeeds.
	r = newRig(t, archive.VerifyChecksum)
	r.src.Put("/h.heic", []byte("heic bytes"))
	rec, err = r.a.Archive(ctx, "/h.heic", archive.ArchiveInput{Thumbnail: []byte("garbage")})
	if err != nil || !rec.Thumb.Placeholder {
		t.Fatalf("placeholder: %v %+v", err, rec.Thumb)
	}
}

// plainDrive hides everything but files.FS.
type plainDrive interface{ files.FS }

func TestNewRequiresObjectStore(t *testing.T) {
	src := archive.Source{FS: fakefs.New(), DriveKey: dk}
	var notStore = struct{ plainDrive }{fakefs.New()}
	if _, err := archive.New(src, archive.Target{FS: notStore, Descriptor: desc}, archive.Options{}); !errors.Is(err, odraz.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	for _, bad := range []string{"", "A/b"} {
		if _, err := archive.New(archive.Source{FS: fakefs.New(), DriveKey: bad}, archive.Target{FS: fakefs.New(), Descriptor: desc}, archive.Options{}); err == nil {
			t.Errorf("drive key %q accepted", bad)
		}
	}
}

func TestLongUnicodePath(t *testing.T) {
	r := newRig(t, archive.VerifyChecksum)
	p := "/" + strings.Repeat("Фотографии Köln 日本/", 40) + strings.Repeat("é", 100) + " +%#?.png"
	r.src.Put(p, r.photo)
	rec, err := r.a.Archive(ctx, p, archive.ArchiveInput{})
	if err != nil || rec.OriginalPath != p {
		t.Fatalf("%v", err)
	}
	back, err := archive.ParseRecord(r.dst.Data(archive.BucketRecordPath(dk, rec.ID)), dk)
	if err != nil || back.OriginalPath != p {
		t.Fatalf("the target's record: %v", err)
	}
}
