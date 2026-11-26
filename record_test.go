package archive_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	archive "github.com/odrazdotxyz/archive-companion"
)

const dk = "drive-1"

func sample(id string) archive.Record {
	taken := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	return archive.Record{
		Version: 1, ID: id,
		Target: archive.RecordTarget{TargetDescriptor: archive.TargetDescriptor{Endpoint: "s3.example.org", Bucket: "b", Prefix: "p"}, Key: archive.ObjectKey(dk, id, "Köln 日本.jpg")},
		SHA256: strings.Repeat("a", 64), Size: 12, Mime: "image/jpeg",
		OriginalPath: "/Fotos/Köln/Köln 日本.jpg", ModTime: taken, TakenAt: &taken, Width: 4, Height: 3,
		Thumb: archive.Thumb{File: id + ".jpg", W: 4, H: 3}, ArchivedAt: taken.Add(time.Hour),
	}
}

func TestRecordRoundTripKeepsUnknownFields(t *testing.T) {
	id := archive.NewID(time.Now())
	data, err := sample(id).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["albumFromTheFuture"] = map[string]any{"name": "x"}
	data, _ = json.Marshal(m)
	r, err := archive.ParseRecord(data, dk)
	if err != nil {
		t.Fatal(err)
	}
	if r.OriginalPath != "/Fotos/Köln/Köln 日本.jpg" || r.Target.Bucket != "b" || r.TakenAt == nil || r.Thumb.W != 4 {
		t.Fatalf("fields lost: %+v", r)
	}
	out, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if got, _ := back["albumFromTheFuture"].(map[string]any); got["name"] != "x" {
		t.Fatalf("unknown field dropped: %s", out)
	}
}

func TestParseRejects(t *testing.T) {
	id, other := archive.NewID(time.Now()), archive.NewID(time.Now().Add(time.Second))
	cases := map[string]func(r *archive.Record){
		"bad id":                func(r *archive.Record) { r.ID = "../x" },
		"lower-case id":         func(r *archive.Record) { r.ID = strings.ToLower(r.ID) },
		"escaping path":         func(r *archive.Record) { r.OriginalPath = "/../x.jpg" },
		"unclean path":          func(r *archive.Record) { r.OriginalPath = "/a//b.jpg" },
		"relative path":         func(r *archive.Record) { r.OriginalPath = "a.jpg" },
		"root":                  func(r *archive.Record) { r.OriginalPath = "/" },
		"reserved folder":       func(r *archive.Record) { r.OriginalPath = "/.odraz/Köln 日本.jpg" },
		"key of another item":   func(r *archive.Record) { r.Target.Key = archive.ObjectKey(dk, other, "Köln 日本.jpg") },
		"key of another drive":  func(r *archive.Record) { r.Target.Key = archive.ObjectKey("other", id, "Köln 日本.jpg") },
		"key with another name": func(r *archive.Record) { r.Target.Key = archive.ObjectKey(dk, id, "else.jpg") },
		"key is a record":       func(r *archive.Record) { r.Target.Key = archive.BucketRecordPath(dk, other) },
		"thumb elsewhere":       func(r *archive.Record) { r.Thumb.File = "../a.jpg" },
		"thumb of another":      func(r *archive.Record) { r.Thumb.File = other + ".jpg" },
		"newer version":         func(r *archive.Record) { r.Version = 2 },
		"no version":            func(r *archive.Record) { r.Version = 0 },
		"upper-case hash":       func(r *archive.Record) { r.SHA256 = strings.Repeat("A", 64) },
		"short hash":            func(r *archive.Record) { r.SHA256 = "ab" },
		"negative size":         func(r *archive.Record) { r.Size = -1 },
	}
	for name, change := range cases {
		r := sample(id)
		change(&r)
		data, err := r.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := archive.ParseRecord(data, dk); !errors.Is(err, archive.ErrBadRecord) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	good, _ := sample(id).Marshal()
	if _, err := archive.ParseRecord(good, dk); err != nil {
		t.Fatalf("the unchanged sample: %v", err)
	}
	for name, data := range map[string][]byte{"not json": []byte("{"), "too big": append(good[:len(good)-1:len(good)-1], []byte(`,"x":"`+strings.Repeat("y", 70<<10)+`"}`)...)} {
		if _, err := archive.ParseRecord(data, dk); !errors.Is(err, archive.ErrBadRecord) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for _, bad := range []string{"", "../x", "A", "a/b", strings.Repeat("a", 65)} {
		if _, err := archive.ParseRecord(good, bad); !errors.Is(err, archive.ErrBadRecord) {
			t.Errorf("drive key %q: err = %v", bad, err)
		}
	}
}
