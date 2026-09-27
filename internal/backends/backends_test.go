package backends

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

// testdata/backends_golden.json is dumped from the Python implementation by a script run with the old interpreter
// from the worktree root (it lived in the session scratchpad, not the repo):
//
//	.venv/bin/python backends_golden.py
//
// It holds json.dumps() text of the Python request bodies, which the Go bodies must reproduce byte for byte.
type golden struct {
	Requests struct {
		FrameB64 string            `json:"frame_b64"`
		CropB64  string            `json:"crop_b64"`
		Context  string            `json:"context"`
		Config   json.RawMessage   `json:"config"`
		Payloads map[string]string `json:"payloads"`
	} `json:"requests"`
	Estimates []struct {
		Backend, Model string
		NWith          int `json:"n_with"`
		NWithout       int `json:"n_without"`
		Estimate       Estimate
	} `json:"estimates"`
	ImageTokens []struct {
		Model  string
		W, H   int
		Tokens int
	} `json:"image_tokens"`
	Prices [][3]any `json:"prices"`
	Shrink struct {
		W        int
		H        int
		LongEdge int `json:"long_edge"`
		Out      [2]int
	} `json:"shrink"`
	ContextText []struct {
		Name, Local, Fixture, Text, Error string
	} `json:"context_text"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("../../testdata/backends_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func goldenItems(t *testing.T, g golden) (Item, Item, pj.Obj) {
	t.Helper()
	frame, _ := base64.StdEncoding.DecodeString(g.Requests.FrameB64)
	crop, _ := base64.StdEncoding.DecodeString(g.Requests.CropB64)
	var cfg pj.Obj
	if err := json.Unmarshal(g.Requests.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	return Item{"42", frame, crop, g.Requests.Context}, Item{"43", frame, nil, g.Requests.Context}, cfg
}

func TestRequestBodiesMatchPython(t *testing.T) {
	g := loadGolden(t)
	item, noCrop, cfg := goldenItems(t, g)
	with := func(k string, v any) pj.Obj {
		c := pj.Clone(cfg)
		c[k] = v
		return c
	}
	t.Setenv("OLLAMA_HOST", "")
	o, gm, an := NewOllama(""), NewGemini(nil), NewAnthropic()
	noMax := with("ollama_schema_max_lengths", false)
	noMax["ollama_num_predict"] = 900.0
	cases := []struct {
		name  string
		be    Backend
		item  Item
		model string
		cfg   pj.Obj
	}{
		{"ollama", o, item, "qwen3-vl:4b-instruct", cfg},
		{"ollama_nocrop_nomax", o, noCrop, "qwen3-vl:8b", noMax},
		{"anthropic_opus", an, item, "claude-opus-5", cfg},
		{"anthropic_haiku_nocrop", an, noCrop, "claude-haiku-4-5", cfg},
		{"anthropic_effort", an, item, "claude-sonnet-5", with("anthropic_effort", "high")},
		{"gemini_3", gm, item, "gemini-3.5-flash-lite", cfg},
		{"gemini_3_nocrop_high", gm, noCrop, "gemini-3.8-flash", with("gemini_thinking_level", "HIGH")},
		{"gemini_2", gm, item, "gemini-2.5-flash", cfg},
	}
	for _, c := range cases {
		want, ok := g.Requests.Payloads[c.name]
		if !ok {
			t.Fatalf("golden lacks %s", c.name)
		}
		req, err := c.be.BuildRequest(c.item, c.model, c.cfg)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := py.Dumps(req); got != want {
			t.Errorf("%s differs from Python:\n got %s\nwant %s", c.name, firstDiff(got, want), firstDiff(want, got))
		}
	}
	if _, err := an.BuildRequest(item, "claude-opus-5", pj.Obj{}); err == nil || err.Error() != "KeyError: 'max_output_tokens'" {
		t.Errorf("missing max_output_tokens: %v", err)
	}
}

// firstDiff shows a's text around the first byte where it differs from b.
func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo, hi := max(0, i-80), min(len(a), i+80)
	return "…" + a[lo:hi] + "…"
}

func TestEstimatesMatchPython(t *testing.T) {
	g := loadGolden(t)
	_, _, cfg := goldenItems(t, g)
	for _, e := range g.Estimates {
		be, err := Get(e.Backend, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := be.Estimate(e.Model, e.NWith, e.NWithout, cfg); got != e.Estimate {
			t.Errorf("%s %s: got %+v want %+v", e.Backend, e.Model, got, e.Estimate)
		}
	}
	for _, c := range g.ImageTokens {
		if got := ImageTokens(c.Model, c.W, c.H); got != c.Tokens {
			t.Errorf("ImageTokens(%s, %d, %d) = %d, want %d", c.Model, c.W, c.H, got, c.Tokens)
		}
	}
	if len(g.Prices) != len(Prices) {
		t.Fatalf("%d prices, want %d", len(Prices), len(g.Prices))
	}
	for i, p := range g.Prices {
		if Prices[i].Model != p[0] || Prices[i].In != p[1] || Prices[i].Out != p[2] {
			t.Errorf("price %d: %+v, want %v", i, Prices[i], p)
		}
	}
}

func TestLoadItem(t *testing.T) {
	g := loadGolden(t)
	dir := t.TempDir()
	var c = g.ContextText[0]
	for _, cc := range g.ContextText {
		if cc.Name == "many_people" { // a hand-made variant with its local inline
			c = cc
		}
	}
	if c.Local == "" || c.Error != "" {
		t.Fatalf("golden case %s has no inline local", c.Name)
	}
	if err := os.WriteFile(filepath.Join(dir, "7.jpg"), []byte("frame"), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := LoadItem(dir, 7, c.Local)
	if err != nil {
		t.Fatal(err)
	}
	if it.Key != "7" || string(it.Frame) != "frame" || it.Crop != nil || it.Context != c.Text {
		t.Errorf("got %+v", it)
	}
	if err := os.WriteFile(filepath.Join(dir, "7_crop.jpg"), []byte("crop"), 0o644); err != nil {
		t.Fatal(err)
	}
	if it, _ = LoadItem(dir, 7, c.Local); string(it.Crop) != "crop" {
		t.Errorf("crop not loaded")
	}
	_, err = LoadItem(dir, 8, c.Local)
	want := "FileNotFoundError: [Errno 2] No such file or directory: '" + filepath.Join(dir, "8.jpg") + "'"
	if err == nil || err.Error() != want {
		t.Errorf("missing frame: %v", err)
	}
	if _, err = LoadItem(dir, 7, `{"height": 1}`); err == nil || err.Error() != "KeyError: 'width'" {
		t.Errorf("bad local: %v", err)
	}
}

func TestGetAndProperties(t *testing.T) {
	if _, err := Get("openai", ""); err == nil || err.Error() != "unknown backend 'openai' (gemini | anthropic | ollama)" {
		t.Errorf("unknown: %v", err)
	}
	for _, c := range []struct {
		name, model string
		sync        bool
		maxBatch    int
	}{{"ollama", "qwen3-vl:4b-instruct", true, 1_000_000_000}, {"gemini", "gemini-3.5-flash-lite", false, 2000},
		{"anthropic", "claude-opus-5", false, 350}} {
		b, err := Get(c.name, "")
		if err != nil {
			t.Fatal(err)
		}
		if b.Name() != c.name || b.DefaultModel() != c.model || b.Sync() != c.sync || b.MaxBatch() != c.maxBatch {
			t.Errorf("%s: %s %s %v %d", c.name, b.Name(), b.DefaultModel(), b.Sync(), b.MaxBatch())
		}
	}
}

func TestRedact(t *testing.T) {
	long := strings.Repeat("QUFB", 2000) // 8000 chars, no spaces
	in := py.NewObject("model", "m", "images", []any{long, "short"}, "raw", []byte(strings.Repeat("x", 3000)),
		"text", strings.Repeat("a b", 2000))
	out := Redact(in).(*py.Object)
	if got := pj.Dumps(out); got != `{"model":"m","images":["<base64 image, 6 KB>","short"],"raw":"<3 KB image>","text":"`+strings.Repeat("a b", 2000)+`"}` {
		t.Errorf("got %.200s", got)
	}
}
