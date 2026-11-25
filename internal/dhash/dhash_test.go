package dhash

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"golang.org/x/image/draw"
)

func gradient() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			v := uint8((x*7 + y*13) % 256)
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

const golden = "f3ce3c73cf9c73e7"

func TestGolden(t *testing.T) {
	got := Hex(Hash(gradient()))
	if got != golden {
		t.Fatalf("Hex(Hash) = %s, want %s", got, golden)
	}
}

func TestHexRoundTrip(t *testing.T) {
	for _, h := range []uint64{0, 1, 0xa1b2c3d4e5f60718, ^uint64(0)} {
		s := Hex(h)
		if len(s) != 16 {
			t.Fatalf("Hex(%d) = %q", h, s)
		}
		back, err := ParseHex(s)
		if err != nil || back != h {
			t.Fatalf("ParseHex(%q) = %d, %v", s, back, err)
		}
	}
	if _, err := ParseHex("zz"); err == nil {
		t.Fatal("want error")
	}
	if _, err := ParseHex("zzzzzzzzzzzzzzzz"); err == nil {
		t.Fatal("want error")
	}
	if Distance(0, 7) != 3 {
		t.Fatal("distance")
	}
}

func TestRobustToJPEGAndResize(t *testing.T) {
	src := gradient()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	dec, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	small := image.NewRGBA(image.Rect(0, 0, 32, 24))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), dec, dec.Bounds(), draw.Src, nil)
	if d := Distance(Hash(src), Hash(small)); d > 4 {
		t.Fatalf("distance %d > 4", d)
	}
}
