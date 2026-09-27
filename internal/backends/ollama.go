package backends

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // PIL opened any format; the cache holds JPEGs
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/image/draw"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/schema"
)

// Ollama is the synchronous backend: one request per image to a local or cluster Ollama server (no API cost).
//
// It works with any vision model Ollama serves (qwen3-vl:4b, qwen3-vl:8b, gemma3, ...), through the native
// /api/chat endpoint with format=<json schema> for constrained JSON output.
type Ollama struct {
	baseURL string
	// HTTP is the client used for requests; its timeout is Python's 600 s.
	HTTP *http.Client
}

// NewOllama makes the backend for baseURL; empty means $OLLAMA_HOST, then http://127.0.0.1:11434. A URL without
// a scheme gets http://; trailing slashes are dropped.
func NewOllama(baseURL string) *Ollama {
	u := baseURL
	if u == "" {
		u = os.Getenv("OLLAMA_HOST")
	}
	if u == "" {
		u = "http://127.0.0.1:11434"
	}
	u = strings.TrimRight(u, "/")
	if !strings.HasPrefix(u, "http") {
		u = "http://" + u
	}
	return &Ollama{baseURL: u, HTTP: &http.Client{Timeout: 600 * time.Second}}
}

func (o *Ollama) Name() string         { return "ollama" }
func (o *Ollama) DefaultModel() string { return "qwen3-vl:4b-instruct" }
func (o *Ollama) Sync() bool           { return true }
func (o *Ollama) MaxBatch() int        { return 1_000_000_000 }
func (o *Ollama) BaseURL() string      { return o.baseURL }

// shrinkFrame downscales a JPEG so its long edge is at most longEdge, re-encoding at quality 82; an image already
// small enough is returned as the original bytes, untouched.
//
// Python used PIL's LANCZOS filter; Go has no Lanczos in x/image/draw, so this uses CatmullRom (the closest, also a
// bicubic-class windowed filter). Dimensions match Python's exactly (round-half-even of w*s, h*s); pixels differ
// slightly, which is acceptable: the image is only model input.
func shrinkFrame(b []byte, longEdge int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, &schema.PyError{Type: "UnidentifiedImageError", Msg: "cannot identify image file <_io.BytesIO object>"}
	}
	w, h := cfg.Width, cfg.Height
	if max(w, h) <= longEdge {
		return b, nil
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, &schema.PyError{Type: "OSError", Msg: err.Error()}
	}
	s := float64(longEdge) / float64(max(w, h))
	nw, nh := max(1, pj.RoundInt(float64(w)*s)), max(1, pj.RoundInt(float64(h)*s))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// BuildRequest is the /api/chat body: system prompt, one user message with the text and base64 images (the frame
// shrunk to cfg ollama_frame_long_edge, default 1024), the answer schema as `format` (with string caps unless
// cfg ollama_schema_max_lengths is false), no thinking, and Qwen3-instruct sampling.
func (o *Ollama) BuildRequest(item Item, model string, cfg pj.Obj) (any, error) {
	frame, err := shrinkFrame(item.Frame, pj.Int(cfgGet(cfg, "ollama_frame_long_edge", 1024)))
	if err != nil {
		return nil, err
	}
	images := []any{base64.StdEncoding.EncodeToString(frame)}
	text := frameLabel
	if len(item.Crop) > 0 {
		images = append(images, base64.StdEncoding.EncodeToString(item.Crop))
		text += "\n" + cropLabel
	}
	text += "\n\n" + userText(item)
	return schema.NewMap(
		"model", model,
		"messages", []any{
			schema.NewMap("role", "system", "content", schema.SystemPrompt),
			schema.NewMap("role", "user", "content", text, "images", images),
		},
		"format", schema.JSONSchema(false, pj.Truthy(cfgGet(cfg, "ollama_schema_max_lengths", true))),
		"stream", false,
		"think", false, // qwen3-vl defaults to thinking, which eats num_predict and leaves content empty
		"keep_alive", "30m",
		// Qwen3 instruct recommended sampling; greedy decoding makes small models loop in free-text fields.
		"options", schema.NewMap("temperature", 0.7, "top_p", 0.8, "top_k", 20, "repeat_penalty", 1.1, "repeat_last_n", 128,
			"num_predict", jsonInt(cfgGet(cfg, "ollama_num_predict", 1536)), "num_ctx", jsonInt(cfgGet(cfg, "ollama_num_ctx", 8192))),
	), nil
}

// httpError is a non-2xx answer (Python's urllib HTTPError).
type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string { return fmt.Sprintf("http %d", e.code) }

// post sends body as Python's json.dumps text and decodes the answer the way json.loads did.
func (o *Ollama) post(ctx context.Context, path string, body any) (pj.Obj, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+path, strings.NewReader(schema.PyDumps(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &httpError{resp.StatusCode, string(raw)}
	}
	v, err := schema.Loads(string(raw))
	if err != nil {
		return nil, err
	}
	obj, ok := floatify(v).(pj.Obj) // numbers as float64, like the rest of the pj code
	if !ok {
		return nil, &schema.PyError{Type: "AttributeError", Msg: fmt.Sprintf("'%s' object has no attribute 'get'", schema.PyTypeName(v))}
	}
	return obj, nil
}

// transportError renders a Go transport error the way Python's urllib reported the equivalent one.
func transportError(err error) string {
	var pe *schema.PyError
	if errors.As(err, &pe) {
		return pe.Error()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "TimeoutError: timed out"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "URLError: <urlopen error [Errno 61] Connection refused>"
	}
	return fmt.Sprintf("URLError: <urlopen error %v>", err)
}

// Classify sends one item and validates the answer. Usage has in/out token counts, wall seconds, the model, and
// prefill/decode seconds and decode tok/s from Ollama's timings. An empty answer from a model that only produced
// thinking is reported as such.
func (o *Ollama) Classify(ctx context.Context, item Item, model string, cfg pj.Obj) Result {
	t := time.Now()
	body, err := o.BuildRequest(item, model, cfg)
	var resp pj.Obj
	if err == nil {
		resp, err = o.post(ctx, "/api/chat", body)
	}
	if err != nil {
		var he *httpError
		if errors.As(err, &he) {
			return Result{Key: item.Key, Error: fmt.Sprintf("ollama http %d: %s", he.code, truncRunes(he.body, 300))}
		}
		return Result{Key: item.Key, Error: "ollama: " + transportError(err)}
	}
	msg := pj.O(resp, "message")
	text := pj.Str(msg["content"])
	ns := func(k string) float64 { return pj.F(pj.Or(resp[k], 0)) / 1e9 }
	var tokS any
	if ns("eval_duration") != 0 {
		tokS = pj.Round(pj.F(pj.Or(resp["eval_count"], 0))/ns("eval_duration"), 1)
	}
	usage := pj.Obj{"in": resp["prompt_eval_count"], "out": resp["eval_count"],
		"seconds": pj.Round(time.Since(t).Seconds(), 2), "model": resp["model"],
		"prefill_s": pj.Round(ns("prompt_eval_duration"), 2), "decode_s": pj.Round(ns("eval_duration"), 2),
		"tok_s": tokS}
	if text == "" && pj.Truthy(msg["thinking"]) {
		return Result{Key: item.Key, Error: "ollama: model spent its output budget thinking; content empty", Usage: usage}
	}
	data, err := schema.ParseAnswer(text)
	if err != nil {
		return Result{Key: item.Key, Error: fmt.Sprintf("ollama parse: %v: %s", err, truncRunes(text, 200)), Usage: usage}
	}
	return Result{Key: item.Key, Data: data, Usage: usage}
}

// truncRunes is Python's s[:n].
func truncRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// Submit is not available: Ollama is synchronous.
func (o *Ollama) Submit(context.Context, []Item, string, pj.Obj, string) (string, error) {
	return "", &schema.PyError{Type: "NotImplementedError", Msg: "ollama backend is synchronous; use classify()"}
}

// Status is always "ended" (there are no batches).
func (o *Ollama) Status(context.Context, string) (string, error) { return "ended", nil }

// Fetch returns nothing (there are no batches).
func (o *Ollama) Fetch(context.Context, string) ([]Result, error) { return nil, nil }

// Estimate counts Qwen3-VL image tokens (one per 32x32 px after patch merging); a local run costs nothing.
func (o *Ollama) Estimate(model string, nWithCrop, nWithout int, cfg pj.Obj) Estimate {
	n := nWithCrop + nWithout
	fw := pj.Int(cfg["frame_long_edge"])
	img := (fw * fw * 2 / 3) / 1024
	if nWithCrop > 0 {
		cs := pj.Int(cfg["crop_size"])
		img += cs * cs / 1024
	}
	return Estimate{Model: model, InputTokens: n * (img + 900), OutputTokens: n * 350, Priced: true}
}
