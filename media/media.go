// Package media tells photos and videos apart and makes their thumbnails. It is
// pure Go: no video is decoded, and formats without a Go decoder (HEIC) get a
// placeholder unless the caller brings an image.
package media

import (
	"bytes"
	"image"
	"path"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// Kind says what a file is.
type Kind int

const (
	None Kind = iota
	Photo
	Video
)

var extensions = map[string]Kind{
	"jpg": Photo, "jpeg": Photo, "png": Photo, "gif": Photo, "webp": Photo, "heic": Photo, "heif": Photo,
	"avif": Photo, "bmp": Photo, "tif": Photo, "tiff": Photo,
	"mp4": Video, "mov": Video, "m4v": Video, "mkv": Video, "webm": Video, "avi": Video, "3gp": Video,
}

// KindOf judges a file by its MIME type and, when that says neither, by the
// extension of its name.
func KindOf(name, mime string) Kind {
	switch m := strings.ToLower(mime); {
	case strings.HasPrefix(m, "image/"):
		return Photo
	case strings.HasPrefix(m, "video/"):
		return Video
	}
	ext := path.Ext(name)
	if ext == path.Base(name) { // ".jpg" is a hidden file without an extension
		return None
	}
	return extensions[strings.ToLower(strings.TrimPrefix(ext, "."))]
}

// Info is what the start of a file tells about a picture. Unknown is zero.
type Info struct {
	Width, Height int // as shown, that is after the EXIF orientation
	TakenAt       *time.Time
}

// Inspect reads Info from the first part of a file (256 KiB is plenty).
func Inspect(head []byte) (info Info) {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(head)); err == nil {
		info.Width, info.Height = cfg.Width, cfg.Height
	}
	x := readEXIF(bytes.NewReader(head))
	if x.orientation >= 5 {
		info.Width, info.Height = info.Height, info.Width
	}
	info.TakenAt = x.taken
	return info
}

type exifData struct {
	orientation int // 1 to 8
	taken       *time.Time
}

func readEXIF(r *bytes.Reader) (d exifData) {
	d.orientation = 1
	defer func() { _ = recover() }() // the parser can panic on damaged data
	x, err := exif.Decode(r)
	if err != nil {
		return d
	}
	if tag, err := x.Get(exif.Orientation); err == nil {
		if v, err := tag.Int(0); err == nil && v >= 1 && v <= 8 {
			d.orientation = v
		}
	}
	if t, err := x.DateTime(); err == nil && !t.IsZero() {
		d.taken = &t
	}
	return d
}
