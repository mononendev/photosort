package backends

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

func TestOllamaBaseURL(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	if u := NewOllama("").BaseURL(); u != "http://127.0.0.1:11434" {
		t.Errorf("default: %s", u)
	}
	t.Setenv("OLLAMA_HOST", "gpu-box:11434/")
	if u := NewOllama("").BaseURL(); u != "http://gpu-box:11434" {
		t.Errorf("env: %s", u)
	}
	if u := NewOllama("https://ollama.example//").BaseURL(); u != "https://ollama.example" {
		t.Errorf("arg: %s", u)
	}
}

func TestShrinkFrame(t *testing.T) {
	g := loadGolden(t)
	img := image.NewRGBA(image.Rect(0, 0, g.Shrink.W, g.Shrink.H))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	out, err := shrinkFrame(buf.Bytes(), g.Shrink.LongEdge)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if [2]int{cfg.Width, cfg.Height} != g.Shrink.Out {
		t.Errorf("shrunk to %dx%d, Python %v", cfg.Width, cfg.Height, g.Shrink.Out)
	}
	// Already small: the original bytes, untouched.
	small, _ := shrinkFrame(out, 1024)
	if !bytes.Equal(small, out) {
		t.Error("small image was re-encoded")
	}
	if _, err := shrinkFrame([]byte("not an image"), 1024); err == nil || !strings.HasPrefix(err.Error(), "UnidentifiedImageError: cannot identify image file") {
		t.Errorf("bad image: %v", err)
	}
}

func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.Gray{200})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const goodAnswer = `{"focus_tier": 3, "focus_notes": "eyes crisp", "primary_subject": "rider_action", "people_count": 1,
"composition": "full_body", "subject_placement": "center", "action": "carving",
"keywords": ["Onewheel", "trail", "dirt", "helmet", "forest"], "adjectives": ["dynamic", "calm", "green"],
"description": "a rider", "quality_remarks": "fine", "quality_score": 4, "keeper": true}`

// ollamaServer answers /api/chat with resp (status 200) or fails with status; it records the last body.
func ollamaServer(t *testing.T, status int, resp string) (*Ollama, *[]byte) {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return NewOllama(srv.URL + "/"), &body
}

func TestOllamaClassify(t *testing.T) {
	item := Item{Key: "5", Frame: tinyJPEG(t), Crop: []byte("crop"), Context: "ctx"}
	cfg := pj.Obj{}
	resp := py.Dumps(py.NewObject("model", "qwen3-vl:4b-instruct", "message", py.NewObject("role", "assistant", "content", goodAnswer),
		"prompt_eval_count", 1200, "eval_count", 180, "prompt_eval_duration", 400_000_000, "eval_duration", 1_234_000_000))
	o, body := ollamaServer(t, 200, resp)
	r := o.Classify(context.Background(), item, "qwen3-vl:4b-instruct", cfg)
	if r.Error != "" || r.Key != "5" {
		t.Fatalf("result %+v", r)
	}
	if r.Data["focus_tier"] != 3.0 || !pj.Equal(r.Data["keywords"], []any{"dirt", "forest", "helmet", "onewheel", "trail"}) {
		t.Errorf("data %v", r.Data)
	}
	u := r.Usage
	if u["in"] != 1200.0 || u["out"] != 180.0 || u["model"] != "qwen3-vl:4b-instruct" || u["prefill_s"] != 0.4 ||
		u["decode_s"] != 1.23 || u["tok_s"] != 145.9 {
		t.Errorf("usage %v", u)
	}
	want, _ := o.BuildRequest(item, "qwen3-vl:4b-instruct", cfg)
	if string(*body) != py.Dumps(want) {
		t.Errorf("request body is not Python's json.dumps of the request")
	}
}

func TestOllamaErrors(t *testing.T) {
	item := Item{Key: "9", Frame: tinyJPEG(t), Context: "ctx"}
	cases := []struct {
		name, resp string
		status     int
		want       string
	}{
		{"http", strings.Repeat("é", 400), 500, "ollama http 500: " + strings.Repeat("é", 300)},
		{"thinking", `{"message": {"content": "", "thinking": "hmm"}, "eval_count": 1536}`, 200,
			"ollama: model spent its output budget thinking; content empty"},
		{"truncated", `{"message": {"content": "{\"focus_tier\": 2, \"focus_notes\": \"soft"}}`, 200,
			`ollama parse: JSONDecodeError: Unterminated string starting at: line 1 column 34 (char 33): {"focus_tier": 2, "focus_notes": "soft`},
		{"missing", `{"message": {"content": "{\"focus_tier\": 2}"}}`, 200,
			`ollama parse: ValueError: missing fields: ['focus_notes', 'primary_subject', 'people_count', 'composition', 'subject_placement', 'action', 'keywords', 'adjectives', 'description', 'quality_remarks', 'quality_score', 'keeper']: {"focus_tier": 2}`},
		{"empty", `{"message": {"content": ""}}`, 200, "ollama parse: JSONDecodeError: Expecting value: line 1 column 1 (char 0): "},
		{"bad body", `<html>`, 200, "ollama: JSONDecodeError: Expecting value: line 1 column 1 (char 0)"},
	}
	for _, c := range cases {
		o, _ := ollamaServer(t, c.status, c.resp)
		r := o.Classify(context.Background(), item, "m", pj.Obj{})
		if r.Error != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, r.Error, c.want)
		}
		if c.name == "thinking" && (r.Usage == nil || r.Usage["out"] != 1536.0 || r.Usage["tok_s"] != nil) {
			t.Errorf("thinking usage %v", r.Usage)
		}
	}
	// Nothing listening.
	o := NewOllama("http://127.0.0.1:1")
	if r := o.Classify(context.Background(), item, "m", pj.Obj{}); !strings.HasPrefix(r.Error, "ollama: URLError: <urlopen error") {
		t.Errorf("refused: %q", r.Error)
	}
	if _, err := o.Submit(context.Background(), nil, "m", nil, ""); err == nil || err.Error() != "NotImplementedError: ollama backend is synchronous; use classify()" {
		t.Errorf("submit: %v", err)
	}
	if st, _ := o.Status(context.Background(), "x"); st != "ended" {
		t.Errorf("status %s", st)
	}
}
