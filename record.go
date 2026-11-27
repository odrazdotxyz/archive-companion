package archive

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/odrazdotxyz/odraz-go-sdk/files"
)

// RecordVersion is the format this code reads and writes.
const RecordVersion = 1

// maxRecord bounds what is read: a record is well under a kilobyte.
const maxRecord = 64 << 10

// TargetDescriptor says which object store an item is in. It never holds
// credentials.
type TargetDescriptor struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
}

// RecordTarget is where an item's original is.
type RecordTarget struct {
	TargetDescriptor
	Key string `json:"key"` // path of the original in the target
}

// Thumb describes the thumbnail beside the drive record.
type Thumb struct {
	File        string `json:"file"`
	W           int    `json:"w"`
	H           int    `json:"h"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

// Record describes one archived item.
type Record struct {
	Version      int          `json:"version"`
	ID           string       `json:"id"`
	Target       RecordTarget `json:"target"`
	SHA256       string       `json:"sha256"`
	Size         int64        `json:"size"`
	Mime         string       `json:"mime"`
	OriginalPath string       `json:"originalPath"`
	ModTime      time.Time    `json:"modTime"`
	TakenAt      *time.Time   `json:"takenAt,omitempty"`
	Width        int          `json:"width,omitempty"`
	Height       int          `json:"height,omitempty"`
	Thumb        Thumb        `json:"thumb"`
	ArchivedAt   time.Time    `json:"archivedAt"`
	// Rebuilt marks a record recreated from the target's copy by Rescan.
	Rebuilt bool `json:"rebuilt,omitempty"`
	// Extra holds fields this version does not know; they are written back.
	Extra map[string]json.RawMessage `json:"-"`
}

var recordFields = []string{"version", "id", "target", "sha256", "size", "mime", "originalPath", "modTime",
	"takenAt", "width", "height", "thumb", "archivedAt", "rebuilt"}

var (
	driveKeyRE = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	sha256RE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Marshal encodes the record, unknown fields included.
func (r Record) Marshal() ([]byte, error) {
	type plain Record
	data, err := json.Marshal(plain(r))
	if err != nil || len(r.Extra) == 0 {
		return data, err
	}
	all := maps.Clone(r.Extra)
	var known map[string]json.RawMessage
	if err := json.Unmarshal(data, &known); err != nil {
		return nil, err
	}
	maps.Copy(all, known) // a known field always wins
	return json.Marshal(all)
}

// ParseRecord reads a record found on the drive or in the target of driveKey.
// Records are untrusted: every path in one is checked, so that acting on it can
// only touch the item it describes.
func ParseRecord(data []byte, driveKey string) (Record, error) {
	var r Record
	bad := func(format string, a ...any) (Record, error) {
		return Record{}, fmt.Errorf("%w: "+format, append([]any{ErrBadRecord}, a...)...)
	}
	if !driveKeyRE.MatchString(driveKey) {
		return bad("drive key %q", driveKey)
	}
	if len(data) > maxRecord {
		return bad("too large")
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return bad("%v", err)
	}
	switch {
	case r.Version != RecordVersion:
		return bad("version %d", r.Version)
	case !validID(r.ID):
		return bad("id %q", r.ID)
	case !validPath(r.OriginalPath):
		return bad("original path %q", r.OriginalPath)
	case r.Target.Key != ObjectKey(driveKey, r.ID, path.Base(r.OriginalPath)):
		return bad("the object %q is not this item's", r.Target.Key)
	case r.Thumb.File != r.ID+".jpg":
		return bad("thumbnail %q", r.Thumb.File)
	case !sha256RE.MatchString(r.SHA256):
		return bad("checksum")
	case r.Size < 0:
		return bad("size %d", r.Size)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return bad("%v", err)
	}
	for _, k := range recordFields {
		delete(all, k)
	}
	if len(all) > 0 {
		r.Extra = all
	}
	return r, nil
}

// validPath reports whether p is a clean drive path of a file of the user's.
func validPath(p string) bool {
	clean, err := files.CleanPath(p)
	if err != nil || clean != p || p == "/" {
		return false
	}
	// In any case of letters: some servers treat them as the same folder.
	top, _, _ := strings.Cut(p[1:], "/")
	return !strings.EqualFold("/"+top, reserved)
}
