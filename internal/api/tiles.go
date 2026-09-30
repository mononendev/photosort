package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/sync/singleflight"
)

// The viewer loads the full-resolution render in tiles: at 1:1 a screen shows a few megapixels of a 24–45 MP frame,
// so only those tiles cross the wire. Tile (z, x, y) covers TileSize<<z native pixels square from (x, y)·that, scaled
// down by 2^z to at most TileSize on a side; z = 0 is native.
const (
	TileSize    = 512
	maxTileZoom = 6
	tileQuality = 90
	decodedKeep = 2 // decoded full renders held in memory (a 45 MP frame is ~70 MB as YCbCr)
)

type decodedFull struct {
	id  int64
	mod time.Time
	img image.Image
}

type fullCache struct {
	render singleflight.Group // one analyzer render per image, however many tiles ask at once
	decode singleflight.Group
	mu     sync.Mutex
	recent []decodedFull
}

func writeHTTPError(w http.ResponseWriter, err error) {
	var he *HTTPError
	if !errors.As(err, &he) {
		he = &HTTPError{500, err.Error()}
	}
	writeJSON(w, he.Code, map[string]any{"detail": he.Detail})
}

// ensureFull renders the original at native resolution into the cache once and returns its path.
func (s *Server) ensureFull(ctx context.Context, id int64) (string, error) {
	p := filepath.Join(s.CacheDir, itoa(id)+"_full.jpg")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	_, err, _ := s.fulls.render.Do(p, func() (any, error) {
		if _, err := os.Stat(p); err == nil {
			return nil, nil
		}
		row, err := s.DB.Row(id)
		if err != nil {
			return nil, errNotFound
		}
		if _, err := os.Stat(row.Path); err != nil {
			return nil, errNotFound
		}
		if s.Analyzer == nil {
			return nil, errf(503, "the analyzer is not running")
		}
		// Not the request's context: the render is shared by every tile waiting on it, and worth keeping if one goes.
		b, err := s.Analyzer.RenderFull(context.WithoutCancel(ctx), row.Path, s.cfg()["exposure"], 92)
		if err != nil {
			return nil, errf(502, "%s", err.Error())
		}
		os.MkdirAll(s.CacheDir, 0o755)
		tmp := fmt.Sprintf("%s.%d.%d.tmp", p, os.Getpid(), time.Now().UnixNano())
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return nil, err
		}
		return nil, os.Rename(tmp, p)
	})
	return p, err
}

// decodedFull is the cached full render, decoded; kept for the last few images so their tiles are only cut and encoded.
func (s *Server) decodedFull(ctx context.Context, id int64) (image.Image, time.Time, error) {
	p, err := s.ensureFull(ctx, id)
	if err != nil {
		return nil, time.Time{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, time.Time{}, errNotFound
	}
	mod := st.ModTime()
	c := &s.fulls
	c.mu.Lock()
	for _, d := range c.recent {
		if d.id == id && d.mod.Equal(mod) {
			c.mu.Unlock()
			return d.img, mod, nil
		}
	}
	c.mu.Unlock()
	v, err, _ := c.decode.Do(p+"@"+mod.String(), func() (any, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, errNotFound
		}
		defer f.Close()
		img, err := jpeg.Decode(f)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		keep := []decodedFull{{id, mod, img}}
		for _, d := range c.recent {
			if d.id != id && len(keep) < decodedKeep {
				keep = append(keep, d)
			}
		}
		c.recent = keep
		c.mu.Unlock()
		return img, nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	return v.(image.Image), mod, nil
}

// tileRect is the native-pixel rectangle tile (z, x, y) covers in a w×h frame; empty when it lies outside.
func tileRect(w, h, z, x, y int) image.Rectangle {
	n := TileSize << z
	return image.Rect(x*n, y*n, (x+1)*n, (y+1)*n).Intersect(image.Rect(0, 0, w, h))
}

func (s *Server) tile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	var zxy [3]int
	for i, k := range []string{"z", "x", "y"} {
		v, err := strconv.Atoi(r.URL.Query().Get(k))
		if err != nil || v < 0 || (k == "z" && v > maxTileZoom) {
			writeHTTPError(w, errf(422, "%s must be an integer in range", k))
			return
		}
		zxy[i] = v
	}
	z, x, y := zxy[0], zxy[1], zxy[2]
	img, mod, err := s.decodedFull(r.Context(), id)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	b := img.Bounds()
	src := tileRect(b.Dx(), b.Dy(), z, x, y).Add(b.Min)
	if src.Empty() {
		writeHTTPError(w, errNotFound)
		return
	}
	var out image.Image
	if z == 0 {
		out = img.(interface {
			SubImage(image.Rectangle) image.Image
		}).SubImage(src)
	} else {
		dw, dh := (src.Dx()+(1<<z)-1)>>z, (src.Dy()+(1<<z)-1)>>z
		dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
		xdraw.BiLinear.Scale(dst, dst.Bounds(), img, src, xdraw.Src, nil)
		out = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: tileQuality}); err != nil {
		writeHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "", mod, bytes.NewReader(buf.Bytes()))
}
