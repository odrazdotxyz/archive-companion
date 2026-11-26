package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/odrazdotxyz/odraz-go-sdk/files"

	"github.com/odrazdotxyz/archive-companion/media"
)

// headSize is how much of a file is kept to read its date and size from.
const headSize = 256 << 10

// capture keeps the first limit bytes written to it.
type capture struct {
	b     bytes.Buffer
	limit int
}

func (c *capture) Write(p []byte) (int, error) {
	if room := c.limit - c.b.Len(); room > 0 {
		c.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// seen is what was learnt from a file while it went by.
type seen struct {
	sum   string
	check files.Check
	head  []byte
	whole []byte // the file itself, if a thumbnail may have to be made from it
}

func (s seen) thumbnail(kind media.Kind) *media.Image {
	if s.whole != nil {
		if img, err := media.Normalize(s.whole); err == nil {
			return &img
		}
	}
	img := media.Placeholder(kind)
	return &img
}

// upload streams the file at p into the object key, once.
func (a *Archive) upload(ctx context.Context, p, key string, st files.Entry, kind media.Kind, needThumb bool, progress func(Stage, int64, int64)) (seen, error) {
	var s seen
	if err := a.dst.FS.Mkdir(ctx, itemFolderOf(key)); err != nil {
		return s, err
	}
	rc, err := a.src.FS.Read(ctx, p, 0, -1)
	if err != nil {
		return s, err
	}
	defer rc.Close()
	s.check = a.store.NewCheck(st.Size)
	hash, count := sha256.New(), &counter{}
	head := &capture{limit: headSize}
	sinks := []io.Writer{hash, count, s.check, head}
	var whole *capture
	if needThumb && kind == media.Photo && st.Size <= media.MaxThumbInput {
		whole = &capture{limit: int(st.Size)}
		sinks = append(sinks, whole)
	}
	err = a.dst.FS.Write(ctx, key, io.TeeReader(rc, io.MultiWriter(sinks...)), files.WriteOptions{
		Size:     st.Size,
		Progress: func(done, total int64) { progress(Uploading, done, total) },
	})
	if err != nil {
		return s, err
	}
	// A source that holds more than it announced is not the file that was sent.
	var one [1]byte
	if n, _ := rc.Read(one[:]); n > 0 || count.n != st.Size {
		return s, fmt.Errorf("archive: %s: the drive sent %d bytes or more, expected %d: %w", p, count.n, st.Size, ErrSourceChanged)
	}
	s.sum, s.head = hex.EncodeToString(hash.Sum(nil)), head.b.Bytes()
	if whole != nil {
		s.whole = whole.b.Bytes()
	}
	return s, nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashFile reads the file at p once, also into w (which may be nil).
func hashFile(ctx context.Context, fs files.FS, p string, w io.Writer) (sum string, n int64, err error) {
	rc, err := fs.Read(ctx, p, 0, -1)
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	h := sha256.New()
	var dst io.Writer = h
	if w != nil {
		dst = io.MultiWriter(h, w)
	}
	n, err = io.Copy(dst, rc)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
