package media_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"runtime"
	"testing"
	"time"

	odraz "github.com/odrazdotxyz/odraz-go-sdk"

	"github.com/odrazdotxyz/archive-companion/media"
)

func TestKindOf(t *testing.T) {
	for _, c := range []struct {
		name, mime string
		want       media.Kind
	}{
		{"a.jpg", "", media.Photo}, {"a.HEIC", "", media.Photo}, {"b.Mp4", "application/octet-stream", media.Video},
		{"x", "image/jpeg", media.Photo}, {"x", "video/quicktime; codecs=x", media.Video},
		{"b.txt", "text/plain", media.None}, {"jpg", "", media.None}, {".jpg", "", media.None}, {"a.jpg.exe", "", media.None},
		{"", "", media.None},
	} {
		if got := media.KindOf(c.name, c.mime); got != c.want {
			t.Errorf("KindOf(%q, %q) = %v, want %v", c.name, c.mime, got, c.want)
		}
	}
}

// quadrants paints red, green, blue, yellow: top left, top right, bottom left, bottom right.
func quadrants(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cols := []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := 0
			if x >= w/2 {
				i++
			}
			if y >= h/2 {
				i += 2
			}
			img.SetRGBA(x, y, cols[i])
		}
	}
	return img
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withEXIF inserts an APP1 segment holding an orientation and the date taken.
func withEXIF(jpg []byte, orientation uint16, taken string) []byte {
	le := binary.LittleEndian
	var t bytes.Buffer
	t.WriteString("II")
	binary.Write(&t, le, uint16(42))
	binary.Write(&t, le, uint32(8))
	// IFD0: orientation, pointer to the Exif IFD
	binary.Write(&t, le, uint16(2))
	binary.Write(&t, le, []uint16{0x0112, 3})
	binary.Write(&t, le, []uint32{1, uint32(orientation)})
	exifAt := uint32(8 + 2 + 2*12 + 4)
	binary.Write(&t, le, []uint16{0x8769, 4})
	binary.Write(&t, le, []uint32{1, exifAt})
	binary.Write(&t, le, uint32(0))
	// Exif IFD: DateTimeOriginal
	binary.Write(&t, le, uint16(1))
	binary.Write(&t, le, []uint16{0x9003, 2})
	binary.Write(&t, le, []uint32{20, exifAt + 2 + 12 + 4})
	binary.Write(&t, le, uint32(0))
	t.WriteString(taken + "\x00")
	body := append([]byte("Exif\x00\x00"), t.Bytes()...)
	seg := []byte{0xFF, 0xE1, byte((len(body) + 2) >> 8), byte(len(body) + 2)}
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, body...)
	return append(out, jpg[2:]...)
}

func decode(t *testing.T, img media.Image) image.Image {
	t.Helper()
	m, err := jpeg.Decode(bytes.NewReader(img.JPEG))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	if m.Bounds().Dx() != img.W || m.Bounds().Dy() != img.H {
		t.Fatalf("size %v, reported %dx%d", m.Bounds(), img.W, img.H)
	}
	return m
}

func near(c color.Color, r, g, b uint32) bool {
	cr, cg, cb, _ := c.RGBA()
	d := func(a, b uint32) bool { a >>= 8; return a+60 > b && b+60 > a }
	return d(cr, r) && d(cg, g) && d(cb, b)
}

func TestThumbnailScalesLongEdgeTo512(t *testing.T) {
	for _, c := range [][4]int{{2000, 1000, 512, 256}, {1000, 3000, 171, 512}, {4000, 3, 512, 1}} {
		got, err := media.Normalize(jpegOf(t, quadrants(c[0], c[1])))
		if err != nil {
			t.Fatal(err)
		}
		decode(t, got)
		if got.W != c[2] || got.H != c[3] || got.Placeholder {
			t.Errorf("%dx%d -> %dx%d", c[0], c[1], got.W, got.H)
		}
	}
}

func TestNoUpscale(t *testing.T) {
	got, err := media.Normalize(jpegOf(t, quadrants(40, 20)))
	if err != nil || got.W != 40 || got.H != 20 {
		t.Fatalf("%dx%d, %v", got.W, got.H, err)
	}
}

func TestTransparencyBecomesWhite(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	got, err := media.Normalize(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if c := decode(t, got).At(4, 4); !near(c, 255, 255, 255) {
		t.Fatalf("colour %v", c)
	}
}

func TestOrientation6And8AreUpright(t *testing.T) {
	src := jpegOf(t, quadrants(80, 40))
	// Where the stored top-left (red) and bottom-left (blue) quadrants end up.
	for orientation, want := range map[uint16]struct{ redX, redY, blueX, blueY int }{
		6: {30, 20, 10, 20}, // turned right: red top right, blue top left
		8: {10, 60, 30, 60}, // turned left: red bottom left, blue bottom right
		3: {60, 30, 60, 10}, // upside down
	} {
		got, err := media.Normalize(withEXIF(src, orientation, "2024:05:01 10:20:30"))
		if err != nil {
			t.Fatal(err)
		}
		m := decode(t, got)
		wantW, wantH := 40, 80
		if orientation == 3 {
			wantW, wantH = 80, 40
		}
		if got.W != wantW || got.H != wantH {
			t.Fatalf("orientation %d: %dx%d", orientation, got.W, got.H)
		}
		if c := m.At(want.redX, want.redY); !near(c, 255, 0, 0) {
			t.Errorf("orientation %d: red expected, got %v", orientation, c)
		}
		if c := m.At(want.blueX, want.blueY); !near(c, 0, 0, 255) {
			t.Errorf("orientation %d: blue expected, got %v", orientation, c)
		}
	}
}

// hugePNG claims 30000 x 30000 pixels in a few bytes.
func hugePNG() []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, 30000)
	binary.BigEndian.PutUint32(ihdr[4:], 30000)
	ihdr[8], ihdr[9] = 8, 6
	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0d")
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

func TestHugeDimensionsRefusedWithoutDecoding(t *testing.T) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := media.Normalize(hugePNG())
	runtime.ReadMemStats(&after)
	if !errors.Is(err, odraz.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Fatalf("allocated %d bytes", grew)
	}
}

func TestUndecodableIsUnsupported(t *testing.T) {
	for name, data := range map[string][]byte{"text": []byte("hello"), "empty": nil, "cut": jpegOf(t, quadrants(100, 100))[:200], "too big": make([]byte, media.MaxThumbInput+1)} {
		if _, err := media.Normalize(data); !errors.Is(err, odraz.ErrUnsupported) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestInspectTakenAt(t *testing.T) {
	got := media.Inspect(withEXIF(jpegOf(t, quadrants(80, 40)), 6, "2024:05:01 10:20:30"))
	if got.Width != 40 || got.Height != 80 {
		t.Errorf("upright size %dx%d", got.Width, got.Height)
	}
	if got.TakenAt == nil || got.TakenAt.Format(time.DateTime) != "2024-05-01 10:20:30" {
		t.Errorf("taken %v", got.TakenAt)
	}
	for _, junk := range [][]byte{nil, []byte("junk"), withEXIF(jpegOf(t, quadrants(8, 8)), 1, "not a date at all!!")[:30], []byte("\xff\xd8\xff\xe1\x00\x10Exif\x00\x00II*\x00\xff\xff\xff\xff")} {
		if got := media.Inspect(junk); got.TakenAt != nil {
			t.Errorf("junk gave a date %v", got.TakenAt)
		}
	}
}

func TestPlaceholderIsJPEG(t *testing.T) {
	photo, video := media.Placeholder(media.Photo), media.Placeholder(media.Video)
	for _, p := range []media.Image{photo, video} {
		decode(t, p)
		if !p.Placeholder || p.W > media.LongEdge || p.H > media.LongEdge {
			t.Errorf("%+v", p)
		}
	}
	if bytes.Equal(photo.JPEG, video.JPEG) {
		t.Error("a video looks like a photo")
	}
}
