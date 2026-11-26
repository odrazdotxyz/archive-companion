package media

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"

	_ "image/gif" // decoders
	_ "image/png"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	LongEdge      = 512         // of a thumbnail, in pixels
	Quality       = 80          // of its JPEG
	MaxPixels     = 100_000_000 // larger pictures are not decoded
	MaxThumbInput = 20 << 20    // larger files are not read for a thumbnail
	exifLimit     = 1 << 20     // how far into a file EXIF is looked for
)

// Image is a thumbnail.
type Image struct {
	JPEG        []byte
	W, H        int
	Placeholder bool
}

// Normalize turns any decodable picture into a thumbnail: an upright JPEG no
// larger than LongEdge. What cannot be decoded gives ErrUnsupported.
func Normalize(data []byte) (Image, error) {
	if len(data) > MaxThumbInput {
		return Image{}, fmt.Errorf("media: picture too large: %w", odraz.ErrUnsupported)
	}
	return FromReader(bytes.NewReader(data))
}

// FromReader is Normalize for a file.
func FromReader(r io.ReadSeeker) (Image, error) {
	unsupported := func(err error) (Image, error) {
		return Image{}, fmt.Errorf("media: cannot make a thumbnail: %v: %w", err, odraz.ErrUnsupported)
	}
	rewind := func() error { _, err := r.Seek(0, io.SeekStart); return err }
	if err := rewind(); err != nil {
		return Image{}, err
	}
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return unsupported(err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return unsupported(fmt.Errorf("%d x %d pixels", cfg.Width, cfg.Height))
	}
	if err := rewind(); err != nil {
		return Image{}, err
	}
	head, err := io.ReadAll(io.LimitReader(r, exifLimit))
	if err != nil {
		return Image{}, err
	}
	orientation := readEXIF(bytes.NewReader(head)).orientation
	if err := rewind(); err != nil {
		return Image{}, err
	}
	src, _, err := image.Decode(r)
	if err != nil {
		return unsupported(err)
	}
	w, h := cfg.Width, cfg.Height
	if long := max(w, h); long > LongEdge {
		w = max((w*LongEdge+long/2)/long, 1)
		h = max((h*LongEdge+long/2)/long, 1)
	}
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(small, small.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(small, small.Bounds(), src, src.Bounds(), draw.Over, nil)
	return encode(upright(small, orientation))
}

func encode(img image.Image) (Image, error) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: Quality}); err != nil {
		return Image{}, err
	}
	return Image{JPEG: b.Bytes(), W: img.Bounds().Dx(), H: img.Bounds().Dy()}, nil
}

// upright applies the EXIF orientation o to the stored picture.
func upright(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int // the stored pixel shown at x, y
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			dst.SetRGBA(x, y, src.RGBAAt(sx, sy))
		}
	}
	return dst
}

// Placeholder is the thumbnail of a file no picture could be made from: a plain
// tile, with a play sign for a video and a landscape for a photo.
func Placeholder(k Kind) Image {
	const w, h = LongEdge, LongEdge * 3 / 4
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{58, 62, 70, 255}), image.Point{}, draw.Src)
	sign := color.RGBA{190, 195, 205, 255}
	cx, cy, s := w/2, h/2, h/5
	for y := cy - s; y <= cy+s; y++ {
		d := y - (cy - s) // 0 at the top of the sign
		for x := cx - s; x <= cx+s; x++ {
			var in bool
			if k == Video { // a triangle pointing right
				off := max(y-cy, cy-y)
				in = x >= cx-s/2 && x <= cx+s-off*3/2
			} else { // a mountain
				in = max(x-cx, cx-x) <= d/2
			}
			if in {
				img.SetRGBA(x, y, sign)
			}
		}
	}
	out, _ := encode(img) // encoding a fresh RGBA image cannot fail
	out.Placeholder = true
	return out
}
