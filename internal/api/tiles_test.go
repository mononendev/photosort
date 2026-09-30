package api

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTiles(t *testing.T) {
	dir := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 1300, 700))
	for y := range 700 {
		for x := range 1300 {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, src, nil)
	os.WriteFile(filepath.Join(dir, "7_full.jpg"), buf.Bytes(), 0o644) // already rendered: no DB or analyzer needed
	h := (&Server{CacheDir: dir}).Handler()

	for _, c := range []struct {
		q    string
		code int
		w, h int
	}{
		{"z=0&x=0&y=0", 200, 512, 512},
		{"z=0&x=2&y=1", 200, 1300 - 1024, 700 - 512}, // the bottom-right corner is a partial tile
		{"z=1&x=0&y=0", 200, 512, 350},
		{"z=1&x=1&y=0", 200, (1300 - 1024 + 1) / 2, 350},
		{"z=3&x=0&y=0", 200, 163, 88},
		{"z=0&x=3&y=0", 404, 0, 0},
		{"z=0&x=-1&y=0", 422, 0, 0},
		{"z=99&x=0&y=0", 422, 0, 0},
		{"x=0&y=0", 422, 0, 0},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/tile/7?"+c.q, nil))
		if rec.Code != c.code {
			t.Errorf("%s: status %d, want %d (%s)", c.q, rec.Code, c.code, rec.Body)
			continue
		}
		if c.code != 200 {
			continue
		}
		cfg, err := jpeg.DecodeConfig(rec.Body)
		if err != nil || cfg.Width != c.w || cfg.Height != c.h {
			t.Errorf("%s: %dx%d (%v), want %dx%d", c.q, cfg.Width, cfg.Height, err, c.w, c.h)
		}
	}
}
