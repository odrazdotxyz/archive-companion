package archive

import (
	"crypto/rand"
	"path"
	"time"

	"github.com/oklog/ulid/v2"
)

// Where the archive keeps things. On the source drive: a record and a thumbnail
// per item under Dir. In the target: the original in a folder of its own, and a
// copy of the record under records/, so the target explains itself.
const (
	reserved = "/.odraz"
	// Dir is the folder of the records and thumbnails on the source drive.
	Dir = reserved + "/archive/photos"
	// targetRoot is the folder of everything the archive writes in a target.
	targetRoot = "/odraz"
)

// NewID returns a new item id: a ULID, which sorts by time.
func NewID(now time.Time) string {
	return ulid.MustNew(ulid.Timestamp(now), rand.Reader).String()
}

func validID(id string) bool {
	u, err := ulid.ParseStrict(id)
	return err == nil && u.String() == id // the canonical spelling only
}

// RecordPath is the drive record of the item id.
func RecordPath(id string) string { return Dir + "/" + id + ".json" }

// ThumbPath is the thumbnail of the item id.
func ThumbPath(id string) string { return Dir + "/" + id + ".jpg" }

func itemFolder(driveKey, id string) string { return targetRoot + "/" + driveKey + "/" + id }

func bucketRecordDir(driveKey string) string { return targetRoot + "/" + driveKey + "/records" }

// ObjectKey is where the original of item id, a file called name, is stored in
// the target.
func ObjectKey(driveKey, id, name string) string { return itemFolder(driveKey, id) + "/" + name }

// BucketRecordPath is the target's copy of the record of item id.
func BucketRecordPath(driveKey, id string) string {
	return bucketRecordDir(driveKey) + "/" + id + ".json"
}

// itemFolderOf is the folder of an object key.
func itemFolderOf(key string) string { return path.Dir(key) }
