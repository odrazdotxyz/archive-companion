//go:build integration

// The archive against real servers. Each source is used if its variables are
// set (see testenv/seed.sh); the target is always an S3 bucket. The test writes
// only in a folder of its own, in /.odraz/archive, and under its own drive key.
package archive_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"github.com/odrazdotxyz/odraz-go-sdk/accounts"
	_ "github.com/odrazdotxyz/odraz-go-sdk/drivers/s3"
	"github.com/odrazdotxyz/odraz-go-sdk/files"
	_ "github.com/odrazdotxyz/odraz-go-sdk/vendors/nextcloud"
	_ "github.com/odrazdotxyz/odraz-go-sdk/vendors/opencloud"

	archive "github.com/odrazdotxyz/archive-companion"
)

func env(t *testing.T, names ...string) []string {
	t.Helper()
	out := make([]string, len(names))
	for i, n := range names {
		if out[i] = os.Getenv(n); out[i] == "" {
			t.Skipf("%s not set", n)
		}
	}
	return out
}

func open(t *testing.T, adapter, method string, cfg map[string]string, pick func(files.Drive) bool) files.FS {
	t.Helper()
	store := accounts.NewMemoryStore()
	acc, err := accounts.Configure(ctx, store, accounts.Account{ID: "it-" + adapter, Label: adapter, Adapter: adapter}, method, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := accounts.Open(ctx, acc, store)
	if err != nil {
		t.Fatal(err)
	}
	client, err := files.ForAccount(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	drives, err := client.Drives(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range drives {
		if pick(d) {
			fs, err := client.OpenDrive(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			return fs
		}
	}
	t.Fatalf("no drive to use among %v", drives)
	return nil
}

func s3Drive(t *testing.T, bucketVar string) (files.FS, archive.TargetDescriptor) {
	t.Helper()
	v := env(t, "ODRAZ_TEST_S3_ENDPOINT", bucketVar, "ODRAZ_TEST_S3_KEY", "ODRAZ_TEST_S3_SECRET")
	prefix := fmt.Sprintf("archive-it-%d", rand.Int63())
	cfg := map[string]string{"endpoint": v[0], "bucket": v[1], "prefix": prefix, "username": v[2], "password": v[3]}
	if r := os.Getenv("ODRAZ_TEST_S3_REGION"); r != "" {
		cfg["region"] = r
	}
	fs := open(t, "s3", "access-key", cfg, func(files.Drive) bool { return true })
	t.Cleanup(func() { _ = fs.Delete(context.Background(), "/odraz", true) })
	return fs, archive.TargetDescriptor{Endpoint: v[0], Bucket: v[1], Prefix: prefix}
}

func personal(d files.Drive) bool { return d.Kind == files.DrivePersonal }

func TestArchiveFromOpenCloud(t *testing.T) {
	v := env(t, "ODRAZ_TEST_OPENCLOUD_URL", "ODRAZ_TEST_OPENCLOUD_USER", "ODRAZ_TEST_OPENCLOUD_TOKEN")
	roundTrip(t, open(t, "opencloud", "app-token", map[string]string{"url": v[0], "username": v[1], "password": v[2], accounts.ConfigInsecure: "true"}, personal))
}

func TestArchiveFromNextcloud(t *testing.T) {
	v := env(t, "ODRAZ_TEST_NEXTCLOUD_URL", "ODRAZ_TEST_NEXTCLOUD_USER", "ODRAZ_TEST_NEXTCLOUD_PASSWORD")
	src := &watched{FS: open(t, "nextcloud", "app-password", map[string]string{"url": v[0], "username": v[1], "password": v[2]}, personal)}
	roundTrip(t, src)
	// docs/format.md promises that this drive deletes only what is unchanged: a
	// fall back to a plain delete would pass everything else here.
	if len(src.answers) != 3 || errors.Join(src.answers...) != nil {
		t.Fatalf("conditional deletes of three originals: %v", src.answers)
	}
	lateEdit(t, src)
}

// watched notes the answers to conditional deletes, and can edit a file just
// before one reaches the server.
type watched struct {
	files.FS
	edit    []byte
	answers []error
}

func (w *watched) Preview(ctx context.Context, p string, width, height int) (io.ReadCloser, string, error) {
	return files.Preview(ctx, w.FS, p, width, height)
}

func (w *watched) DeleteIfMatch(ctx context.Context, p, etag string) error {
	if w.edit != nil {
		if err := w.FS.Write(ctx, p, bytes.NewReader(w.edit), files.WriteOptions{Size: int64(len(w.edit)), Overwrite: true}); err != nil {
			return err
		}
	}
	d, ok := w.FS.(files.ConditionalDeleter)
	if !ok {
		return errors.New("the drive has no conditional delete")
	}
	err := d.DeleteIfMatch(ctx, p, etag)
	w.answers = append(w.answers, err)
	return err
}

// lateEdit changes a file as it is being deleted: the change must be kept.
func lateEdit(t *testing.T, src *watched) {
	t.Helper()
	dst, desc := s3Drive(t, "ODRAZ_TEST_S3_BUCKET")
	folder := fmt.Sprintf("/archive-test-%d", rand.Int63())
	if err := src.Mkdir(ctx, folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Delete(context.Background(), folder, true) })
	a, err := archive.New(archive.Source{FS: src, DriveKey: fmt.Sprintf("it-%d", rand.Int63())}, archive.Target{FS: dst, Descriptor: desc}, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p, photo := folder+"/late.jpg", testJPEG()
	if err := src.Write(ctx, p, bytes.NewReader(photo), files.WriteOptions{Size: int64(len(photo))}); err != nil {
		t.Fatal(err)
	}
	src.edit = []byte("edited at the last moment")
	rec, err := a.Archive(ctx, p, archive.ArchiveInput{})
	if err == nil { // the records are outside the folder removed above
		_ = src.Delete(ctx, archive.RecordPath(rec.ID), false)
		_ = src.Delete(ctx, archive.ThumbPath(rec.ID), false)
	}
	if !errors.Is(err, archive.ErrSourceChanged) || !errors.Is(src.answers[len(src.answers)-1], odraz.ErrChanged) {
		t.Fatalf("late edit: %v, server: %v", err, src.answers)
	}
	if !bytes.Equal(content(t, src, p), src.edit) {
		t.Fatal("the late edit was not kept")
	}
	if list, problems, err := a.List(ctx); err != nil || len(list)+len(problems) != 0 {
		t.Fatalf("left listed: %v %v %v", list, problems, err)
	}
}

func TestArchiveFromS3(t *testing.T) {
	fs, _ := s3Drive(t, "ODRAZ_TEST_S3_SOURCE_BUCKET")
	t.Cleanup(func() { _ = fs.Delete(context.Background(), "/.odraz", true) })
	roundTrip(t, fs)
}

func testJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 1200; x++ {
			img.SetRGBA(x, y, color.RGBA{byte(x), byte(y), byte(x ^ y), 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func content(t *testing.T, fs files.FS, p string) []byte {
	t.Helper()
	rc, err := fs.Read(ctx, p, 0, -1)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return b
}

func gone(t *testing.T, fs files.FS, p string) {
	t.Helper()
	if _, err := fs.Stat(ctx, p); err == nil {
		t.Fatalf("%s is still there", p)
	}
}

func roundTrip(t *testing.T, src files.FS) {
	dst, desc := s3Drive(t, "ODRAZ_TEST_S3_BUCKET")
	folder := fmt.Sprintf("/archive-test-%d", rand.Int63())
	if err := src.Mkdir(ctx, folder+"/Köln 日本"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	t.Cleanup(func() {
		c := context.Background()
		for _, id := range ids {
			_ = src.Delete(c, archive.RecordPath(id), false)
			_ = src.Delete(c, archive.ThumbPath(id), false)
		}
		_ = src.Delete(c, folder, true)
	})
	a, err := archive.New(archive.Source{FS: src, DriveKey: fmt.Sprintf("it-%d", rand.Int63())}, archive.Target{FS: dst, Descriptor: desc}, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	video := make([]byte, 40<<20)
	rand.New(rand.NewSource(40)).Read(video)
	data := map[string][]byte{
		folder + "/Köln 日本/Bild Ü +%#.jpg": testJPEG(),
		folder + "/clip.mp4":               video,
		folder + "/delete-me.jpg":          testJPEG(),
	}
	records := map[string]archive.Record{}
	for p, d := range data {
		if err := src.Write(ctx, p, bytes.NewReader(d), files.WriteOptions{Size: int64(len(d))}); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if rc, mime, err := files.Preview(ctx, src, p, 512, 512); err == nil {
			rc.Close()
			t.Logf("server preview of %s: %s", p[len(folder):], mime)
		} else {
			t.Logf("no server preview of %s: %v", p[len(folder):], err)
		}
		rec, err := a.Archive(ctx, p, archive.ArchiveInput{})
		if err != nil {
			t.Fatalf("archive %s: %v", p, err)
		}
		ids = append(ids, rec.ID)
		records[p] = rec
		gone(t, src, p)
		if !bytes.Equal(content(t, dst, rec.Target.Key), d) {
			t.Fatalf("%s: the object differs", p)
		}
		if strings.HasSuffix(p, ".jpg") && rec.Thumb.Placeholder {
			t.Errorf("%s: a photo got a placeholder", p)
		}
	}
	list, problems, err := a.List(ctx)
	if err != nil || len(problems) != 0 || len(list) != len(data) {
		t.Fatalf("list: %d, %v, %v", len(list), problems, err)
	}

	// A lost record comes back from the target.
	photo := folder + "/Köln 日本/Bild Ü +%#.jpg"
	if err := src.Delete(ctx, archive.RecordPath(records[photo].ID), false); err != nil {
		t.Fatal(err)
	}
	res, err := a.Rescan(ctx)
	if err != nil || len(res.Rebuilt) != 1 || res.Rebuilt[0].OriginalPath != photo || len(res.Problems) != 0 {
		t.Fatalf("rescan: %+v %v", res, err)
	}

	// Permanent delete.
	del := records[folder+"/delete-me.jpg"]
	if err := a.Delete(ctx, del.ID); err != nil {
		t.Fatal(err)
	}
	gone(t, dst, del.Target.Key)
	gone(t, dst, archive.BucketRecordPath(strings.Split(del.Target.Key, "/")[2], del.ID))
	gone(t, src, archive.RecordPath(del.ID))

	// Restore the rest.
	for _, p := range []string{photo, folder + "/clip.mp4"} {
		got, err := a.Restore(ctx, records[p].ID, archive.RestoreInput{})
		if err != nil || got.Path != p {
			t.Fatalf("restore %s: %+v %v", p, got, err)
		}
		if !bytes.Equal(content(t, src, p), data[p]) {
			t.Fatalf("%s: restored content differs", p)
		}
		gone(t, dst, records[p].Target.Key)
	}
	if list, _, _ := a.List(ctx); len(list) != 0 {
		t.Fatalf("still listed: %d", len(list))
	}

	// The manifest, if the drive has none of its own.
	if _, ok, err := archive.ReadTarget(ctx, src); err == nil && !ok {
		if err := archive.SetTarget(ctx, src, desc); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := archive.ReadTarget(ctx, src); err != nil || !ok || got != desc {
			t.Fatalf("manifest: %+v %v %v", got, ok, err)
		}
		if err := archive.RemoveTarget(ctx, src); err != nil {
			t.Fatal(err)
		}
	}
}
