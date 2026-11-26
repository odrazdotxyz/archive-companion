package archive_test

import (
	"testing"
	"time"

	archive "github.com/odrazdotxyz/archive-companion"
)

func TestPaths(t *testing.T) {
	id := "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	for got, want := range map[string]string{
		archive.RecordPath(id):                      "/.odraz/archive/photos/" + id + ".json",
		archive.ThumbPath(id):                       "/.odraz/archive/photos/" + id + ".jpg",
		archive.ObjectKey("dk", id, "é 日本 +%#.jpg"): "/odraz/dk/" + id + "/é 日本 +%#.jpg",
		archive.BucketRecordPath("dk", id):          "/odraz/dk/records/" + id + ".json",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestNewIDsSortByTime(t *testing.T) {
	a := archive.NewID(time.Unix(1000, 0))
	b := archive.NewID(time.Unix(2000, 0))
	if len(a) != 26 || !(a < b) || a == archive.NewID(time.Unix(1000, 0)) {
		t.Fatalf("a=%s b=%s", a, b)
	}
}
