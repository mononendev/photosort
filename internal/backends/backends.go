// Package backends sends analyzed images to a vision model and brings back its structured answer: Ollama
// (synchronous, local, free), and the Gemini and Anthropic batch APIs (submit now, poll, fetch later).
//
// It is a port of photosort/backends and must stay behaviour-identical: same prompts, same request bodies (key
// order included; BuildRequest returns them as py.Object for the UI's request view and the dry run), same
// validation and the same error strings stored for failed images.
package backends

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
	"github.com/mononendev/photosort/internal/schema"
)

// Item is the request for one analyzed image.
type Item struct {
	Key     string // the image id as a string; results come back under it
	Frame   []byte // the cached, downscaled full frame (JPEG)
	Crop    []byte // native-resolution crop around the primary person's head and upper body; nil when none
	Context string // schema.ContextText of the image's local_json
}

// Result is a backend's answer for one image: Data (validated) or Error, plus whatever usage it reported.
type Result struct {
	Key   string
	Data  pj.Obj
	Usage pj.Obj
	Error string
}

// Estimate is a token and cost estimate for a run.
type Estimate struct {
	Model          string  `json:"model"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	InteractiveUSD float64 `json:"interactive_usd"`
	BatchUSD       float64 `json:"batch_usd"`
	Priced         bool    `json:"priced"` // false when the model is not in Prices (cost shows 0)
}

// Backend is one vision-model provider. Sync backends answer one image at a time through Classify; batch backends
// Submit a batch, report its Status, and Fetch the results once it has ended.
type Backend interface {
	Name() string
	DefaultModel() string
	// Sync is true when Classify answers one item at a time instead of batch Submit/poll.
	Sync() bool
	// MaxBatch caps the number of items per Submit.
	MaxBatch() int
	// BaseURL is the server a sync backend talks to ("" for the cloud batch backends).
	BaseURL() string
	// BuildRequest is the exact request body for one item (images base64-encoded), as ordered JSON.
	BuildRequest(item Item, model string, cfg pj.Obj) (any, error)
	// Classify sends one item and waits for the answer (sync backends only).
	Classify(ctx context.Context, item Item, model string, cfg pj.Obj) Result
	// Submit starts a batch job and returns its id (batch backends only).
	Submit(ctx context.Context, items []Item, model string, cfg pj.Obj, workdir string) (string, error)
	// Status is "running", "ended" or "failed:...".
	Status(ctx context.Context, batchID string) (string, error)
	// Fetch returns the results of an ended batch.
	Fetch(ctx context.Context, batchID string) ([]Result, error)
	// Estimate is the token/cost estimate for nWithCrop images with a crop and nWithout without.
	Estimate(model string, nWithCrop, nWithout int, cfg pj.Obj) Estimate
}

// Price is interactive USD per 1M tokens; batch is half.
type Price struct {
	Model   string
	In, Out float64
}

// Prices is the price table, in the order the CLI lists it. Checked 2026-09-25 against provider pricing pages.
var Prices = []Price{
	{"gemini-2.5-flash-lite", 0.10, 0.40},
	{"gemini-2.5-flash", 0.30, 2.50},
	{"gemini-3.1-flash-lite", 0.25, 1.50},
	{"gemini-3.5-flash-lite", 0.30, 2.50},
	{"gemini-3.5-flash", 1.50, 9.00},
	{"gemini-3.8-flash", 0.75, 3.75},
	{"claude-haiku-4-5", 1.00, 5.00},
	{"claude-sonnet-5", 2.00, 10.00},
	{"claude-opus-5", 5.00, 25.00},
}

// PriceFor looks model up in Prices.
func PriceFor(model string) (Price, bool) {
	for _, p := range Prices {
		if p.Model == model {
			return p, true
		}
	}
	return Price{}, false
}

const (
	promptTokens = 900 // system prompt + detector context, approx
	outputTokens = 350 // JSON with keywords/remarks, approx
)

// ImageTokens approximates the token cost of one image at w x h pixels.
func ImageTokens(model string, w, h int) int {
	switch {
	case strings.HasPrefix(model, "gemini-3"):
		return 1120 // flat per image at MEDIA_RESOLUTION_HIGH; 560 at MEDIUM
	case strings.HasPrefix(model, "gemini"):
		if max(w, h) <= 384 {
			return 258
		}
		unit := max(1, int(float64(min(w, h))/1.5))
		return 258 * ceilDiv(w, unit) * ceilDiv(h, unit)
	case strings.HasPrefix(model, "claude"):
		t := float64(w*h) / 750
		capT := 4784.0
		if strings.Contains(model, "haiku") {
			capT = 1600
		}
		return int(math.Min(t, capT))
	}
	return int(float64(w*h) / 750)
}

func ceilDiv(a, b int) int { return -floorDiv(-a, b) }

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// batchEstimate is the cloud backends' estimate (Python's Backend.estimate).
func batchEstimate(model string, nWithCrop, nWithout int, cfg pj.Obj) Estimate {
	fw := pj.Int(cfg["frame_long_edge"])
	frameT := ImageTokens(model, fw, int(float64(fw)*2/3))
	cs := pj.Int(cfg["crop_size"])
	cropT := ImageTokens(model, cs, cs)
	if strings.HasPrefix(model, "gemini-3") {
		cropT = 560 // crops are sent at MEDIUM on Gemini 3
	}
	inp := nWithCrop*(frameT+cropT+promptTokens) + nWithout*(frameT+promptTokens)
	out := (nWithCrop + nWithout) * outputTokens
	p, priced := PriceFor(model)
	cost := (float64(inp)*p.In + float64(out)*p.Out) / 1e6
	return Estimate{Model: model, InputTokens: inp, OutputTokens: out,
		InteractiveUSD: pj.Round(cost, 2), BatchUSD: pj.Round(cost/2, 2), Priced: priced}
}

// LoadItem is the request for one analyzed image: its cached frame ({id}.jpg) and crop ({id}_crop.jpg, optional)
// from cacheDir, and the detector summary built from its local_json. A missing frame fails like Python's
// FileNotFoundError; bad local_json like its JSONDecodeError/KeyError.
func LoadItem(cacheDir string, id int64, localJSON string) (Item, error) {
	key := strconv.FormatInt(id, 10)
	fp := filepath.Join(cacheDir, key+".jpg")
	frame, err := os.ReadFile(fp)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Item{}, &py.Error{Type: "FileNotFoundError", Msg: "[Errno 2] No such file or directory: " + py.Repr(fp)}
		}
		return Item{}, err
	}
	var crop []byte
	cp := filepath.Join(cacheDir, key+"_crop.jpg")
	if _, err := os.Stat(cp); err == nil {
		if crop, err = os.ReadFile(cp); err != nil {
			return Item{}, err
		}
	}
	local, err := py.LoadsNumber(localJSON)
	if err != nil {
		return Item{}, err
	}
	obj, _ := floatify(local).(pj.Obj)
	ctx, err := schema.ContextText(obj)
	if err != nil {
		return Item{}, err
	}
	return Item{Key: key, Frame: frame, Crop: crop, Context: ctx}, nil
}

// floatify turns json.Number into float64: local_json is read like pj.Parse reads it (see schema.ContextText for
// why that is the robust choice).
func floatify(v any) any {
	switch x := v.(type) {
	case pj.Obj:
		for k, e := range x {
			x[k] = floatify(e)
		}
	case []any:
		for i, e := range x {
			x[i] = floatify(e)
		}
	default:
		if f, ok := pj.Float(v); ok {
			if _, isBool := v.(bool); !isBool {
				return f
			}
		}
	}
	return v
}

// UnknownBackendError is returned by Get for a name it doesn't know.
type UnknownBackendError struct{ Name string }

func (e *UnknownBackendError) Error() string {
	return fmt.Sprintf("unknown backend %s (gemini | anthropic | ollama)", py.Repr(e.Name))
}

// Get returns the backend called name. baseURL only applies to Ollama (empty: OLLAMA_HOST, then the default).
func Get(name, baseURL string) (Backend, error) {
	switch name {
	case "gemini":
		return NewGemini(nil), nil
	case "anthropic":
		return NewAnthropic(), nil
	case "ollama":
		return NewOllama(baseURL), nil
	}
	return nil, &UnknownBackendError{name}
}

// Redact replaces what a request view should not print in full: long base64 strings (over 4000 characters, no
// space) become "<base64 image, N KB>" and raw bytes "<N KB image>". Order is kept. This is the web app's
// vlm-request redaction.
func Redact(v any) any {
	switch x := v.(type) {
	case *py.Object:
		out := py.NewObject()
		for _, k := range x.Keys() {
			out.Set(k, Redact(x.Get(k)))
		}
		return out
	case pj.Obj:
		out := make(pj.Obj, len(x))
		for k, e := range x {
			out[k] = Redact(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Redact(e)
		}
		return out
	case []byte:
		return fmt.Sprintf("<%s KB image>", strconv.FormatFloat(float64(len(x))/1024, 'f', 0, 64))
	case string:
		if len([]rune(x)) > 4000 && !strings.Contains(x, " ") {
			return fmt.Sprintf("<base64 image, %s KB>", strconv.FormatFloat(float64(len([]rune(x)))*3/4/1024, 'f', 0, 64))
		}
	}
	return v
}

// cfgGet is Python's cfg.get(k, def): the stored value when the key is present (even if null), else def.
func cfgGet(cfg pj.Obj, k string, def any) any {
	if v, ok := cfg[k]; ok {
		return v
	}
	return def
}

// cfgNeed is Python's cfg[k].
func cfgNeed(cfg pj.Obj, k string) (any, error) {
	v, ok := cfg[k]
	if !ok {
		return nil, py.KeyError(k)
	}
	return jsonInt(v), nil
}

// jsonInt makes an integral float64 (how a decoded config holds Python's ints) an int, so the request body
// carries 1024 rather than 1024.0.
func jsonInt(v any) any {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return int64(f)
	}
	return v
}

// userText is the closing text part every backend sends after the images.
func userText(item Item) string {
	return item.Context + "\n\nAnalyze the photo and return the JSON object."
}

const (
	frameLabel = "Image 1: full frame."
	cropLabel  = "Image 2: native-resolution crop of the primary subject's head and upper body."
)

// errBatchOnly is Classify's answer on a batch backend (Python had no classify there; the pipeline checks Sync).
func errBatchOnly(name, key string) Result {
	return Result{Key: key, Error: fmt.Sprintf("backend %s is batch-only; use the CLI submit/poll for it", name)}
}
