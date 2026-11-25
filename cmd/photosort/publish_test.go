package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestParseSubSecMS(t *testing.T) {
	ptr := func(n int) *int { return &n }
	for _, c := range []struct {
		in   string
		want *int
	}{
		{"94", ptr(940)}, {"5", ptr(500)}, {"05", ptr(50)}, {"1234", ptr(123)}, {" 94 ", ptr(940)},
		{"9x", ptr(900)}, {"", nil}, {"  ", nil}, {"abc", nil},
	} {
		got := parseSubSecMS(c.in)
		if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("parseSubSecMS(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPublish(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	folder := filepath.Join(root, "RAW", "event")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range []string{"a.jpg", "b.jpg"} {
		p := filepath.Join(folder, n)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	t.Setenv("PHOTOSORT_PHOTOS", root)
	t.Setenv("PHOTOSORT_DB", "")
	oldWorkdir := workdir
	workdir = work
	defer func() { workdir = oldWorkdir }()

	a, err := openApp()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.AddPaths(paths); err != nil {
		t.Fatal(err)
	}
	taken := []string{"2026:04:23 09:03:23", "2026:04:23 09:03:24"}
	for i, p := range paths {
		rows, err := a.db.RowsUnder([]string{p}, "", nil, "")
		if err != nil || len(rows) != 1 {
			t.Fatalf("row %s: %v", p, err)
		}
		js := `{"local_tier":3,"n_people":1,"exif":{"taken":"` + taken[i] + `","camera":"Canon","f_number":1.2,"iso":100}}`
		if err := a.db.SetLocal(rows[0].ID, &js, nil); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			img := image.NewGray(image.Rect(0, 0, 64, 48))
			for x := 0; x < 64; x++ {
				for y := 0; y < 48; y++ {
					img.SetGray(x, y, color.Gray{uint8(x*7 + y*13)})
				}
			}
			if err := os.MkdirAll(a.cacheDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(a.cacheDir(), strconv.FormatInt(rows[0].ID, 10)+"_thumb.jpg"), buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	a.close()

	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/admin-api/events/ev/sources":
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			w.Write([]byte(`{"upserted":2}`))
		case r.Method == "POST" && r.URL.Path == "/admin-api/events/ev/sync":
			w.WriteHeader(202)
			w.Write([]byte(`{"sync_state":"running"}`))
		case r.Method == "GET" && r.URL.Path == "/admin-api/events/ev/sync":
			w.Write([]byte(`{"sync_state":"done","sync_result":{"items":5,"matched_time":2,"matched_time_hash":1,"matched_hash":1,"unmatched":1}}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"detail":"nope"}`))
		}
	}))
	defer srv.Close()
	oldPoll := publishPollEvery
	publishPollEvery = time.Millisecond
	defer func() { publishPollEvery = oldPoll }()

	cmd := publishCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--event", "ev", "--folder", "RAW/event", "--gallery", srv.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("sources requests = %d", len(bodies))
	}
	var req struct {
		Photos []struct {
			RelPath string  `json:"rel_path"`
			Taken   string  `json:"taken"`
			DHash   *string `json:"dhash"`
			Subject *string `json:"subject"`
			Comp    *string `json:"composition"`
		} `json:"photos"`
	}
	if err := json.Unmarshal(bodies[0], &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Photos) != 2 {
		t.Fatalf("photos = %d", len(req.Photos))
	}
	hex := regexp.MustCompile(`^[0-9a-f]{16}$`)
	withHash := 0
	for _, p := range req.Photos {
		if p.DHash != nil {
			withHash++
			if !hex.MatchString(*p.DHash) {
				t.Errorf("dhash %q", *p.DHash)
			}
		}
		if p.Subject != nil && *p.Subject == "unknown" || p.Comp != nil && *p.Comp == "unknown" {
			t.Errorf("unknown sent for %s: %v %v", p.RelPath, p.Subject, p.Comp)
		}
		want := map[string]string{"RAW/event/a.jpg": taken[0], "RAW/event/b.jpg": taken[1]}[p.RelPath]
		if want == "" || p.Taken != want {
			t.Errorf("photo %q taken %q", p.RelPath, p.Taken)
		}
	}
	if withHash != 1 {
		t.Errorf("photos with a dhash = %d", withHash)
	}
	if !bytes.Contains(bodies[0], []byte(`"dhash":null`)) {
		t.Error("missing dhash is not null")
	}
	const want = "synced ev: 5 album photos, 2 matched by time, 1 by time+hash, 1 by hash, 1 unmatched\n"
	if !bytes.HasSuffix(out.Bytes(), []byte(want)) {
		t.Errorf("output %q", out.String())
	}
}
