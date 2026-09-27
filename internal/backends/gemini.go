package backends

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"google.golang.org/genai"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/schema"
)

// Gemini is the Gemini Batch API backend: requests go up as a JSONL file through the Files API, and results come
// back as a JSONL file (or inlined in the job).
type Gemini struct {
	clientConfig *genai.ClientConfig
	once         sync.Once
	client       *genai.Client
	clientErr    error
}

// NewGemini makes the backend. cc configures the client; nil takes everything from the environment
// (GEMINI_API_KEY / GOOGLE_API_KEY), like Python's genai.Client(). The client is created on first use.
func NewGemini(cc *genai.ClientConfig) *Gemini { return &Gemini{clientConfig: cc} }

func (g *Gemini) Name() string         { return "gemini" }
func (g *Gemini) DefaultModel() string { return "gemini-3.5-flash-lite" }
func (g *Gemini) Sync() bool           { return false }
func (g *Gemini) MaxBatch() int        { return 2000 } // 2 GB file limit; ~0.6 MB per request
func (g *Gemini) BaseURL() string      { return "" }

func (g *Gemini) getClient(ctx context.Context) (*genai.Client, error) {
	g.once.Do(func() {
		cc := g.clientConfig
		if cc == nil {
			cc = &genai.ClientConfig{}
		}
		g.client, g.clientErr = genai.NewClient(ctx, cc)
	})
	return g.client, g.clientErr
}

// BuildRequest is one JSONL line of the batch input, {key, request}, with the same structure and key order as the
// Python SDK's to_json_dict() output (snake_case keys, URL-safe base64 image data). Gemini 3 models get per-part
// media resolution (HIGH for the frame, MEDIUM for the crop) and a thinking level (cfg gemini_thinking_level,
// default LOW); older models get thinking_budget 0.
func (g *Gemini) BuildRequest(item Item, model string, cfg pj.Obj) (any, error) {
	g3 := strings.HasPrefix(model, "gemini-3")
	img := func(b []byte, level string) *schema.Map {
		p := schema.NewMap()
		if g3 {
			p.Set("media_resolution", schema.NewMap("level", level))
		}
		// pydantic serializes bytes as URL-safe base64 (with padding).
		return p.Set("inline_data", schema.NewMap("data", base64.URLEncoding.EncodeToString(b), "mime_type", "image/jpeg"))
	}
	text := func(s string) *schema.Map { return schema.NewMap("text", s) }
	parts := []any{text(frameLabel), img(item.Frame, "MEDIA_RESOLUTION_HIGH")}
	if len(item.Crop) > 0 {
		parts = append(parts, text(cropLabel), img(item.Crop, "MEDIA_RESOLUTION_MEDIUM"))
	}
	parts = append(parts, text(userText(item)))

	maxOut, err := cfgNeed(cfg, "max_output_tokens")
	if err != nil {
		return nil, err
	}
	thinking := schema.NewMap("thinking_budget", 0)
	if g3 {
		thinking = schema.NewMap("thinking_level", cfgGet(cfg, "gemini_thinking_level", "LOW"))
	}
	gen := schema.NewMap(
		"response_json_schema", schema.JSONSchema(false, false),
		"max_output_tokens", maxOut,
		"response_mime_type", "application/json",
		"thinking_config", thinking,
	)
	return schema.NewMap(
		"key", item.Key,
		"request", schema.NewMap(
			"system_instruction", schema.NewMap("parts", []any{text(schema.SystemPrompt)}),
			"contents", []any{schema.NewMap("parts", parts, "role", "user")},
			"generation_config", gen,
		),
	), nil
}

// WriteJSONL writes one request per line, each line exactly as Python's json.dumps wrote it (this is what
// `photosort submit --dry-run` writes and what Submit uploads).
func (g *Gemini) WriteJSONL(items []Item, model string, cfg pj.Obj, path string) error {
	var b strings.Builder
	for _, it := range items {
		r, err := g.BuildRequest(it, model, cfg)
		if err != nil {
			return err
		}
		b.WriteString(schema.PyDumps(r))
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// Submit writes workdir/batches/gemini-<stamp>.jsonl, uploads it, and creates the batch job; it returns the job
// name.
func (g *Gemini) Submit(ctx context.Context, items []Item, model string, cfg pj.Obj, workdir string) (string, error) {
	c, err := g.getClient(ctx)
	if err != nil {
		return "", err
	}
	bdir := filepath.Join(workdir, "batches")
	if err := os.Mkdir(bdir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	stem := "gemini-" + stamp
	path := filepath.Join(bdir, stem+".jsonl")
	if err := g.WriteJSONL(items, model, cfg, path); err != nil {
		return "", err
	}
	up, err := c.Files.UploadFromPath(ctx, path, &genai.UploadFileConfig{DisplayName: stem, MIMEType: "jsonl"})
	if err != nil {
		return "", err
	}
	job, err := c.Batches.Create(ctx, model, &genai.BatchJobSource{FileName: up.Name},
		&genai.CreateBatchJobConfig{DisplayName: "photosort-" + stamp})
	if err != nil {
		return "", err
	}
	return job.Name, nil
}

// Status maps the job state: succeeded or partially succeeded is "ended"; failed, cancelled or expired is
// "failed:<STATE>:<error>"; anything else "running".
func (g *Gemini) Status(ctx context.Context, batchID string) (string, error) {
	c, err := g.getClient(ctx)
	if err != nil {
		return "", err
	}
	job, err := c.Batches.Get(ctx, batchID, nil)
	if err != nil {
		return "", err
	}
	return geminiStatus(job), nil
}

func geminiStatus(job *genai.BatchJob) string {
	switch st := string(job.State); st {
	case "JOB_STATE_SUCCEEDED", "JOB_STATE_PARTIALLY_SUCCEEDED":
		return "ended"
	case "JOB_STATE_FAILED", "JOB_STATE_CANCELLED", "JOB_STATE_EXPIRED":
		return fmt.Sprintf("failed:%s:%s", st, jobErrorStr(job.Error))
	}
	return "running"
}

// jobErrorStr is str() of the Python SDK's JobError (a pydantic model: "details=None code=3 message='...'").
func jobErrorStr(e *genai.JobError) string {
	if e == nil {
		return "None"
	}
	var details any
	if e.Details != nil {
		details = e.Details
	}
	var code any
	if e.Code != nil {
		code = int64(*e.Code)
	}
	var msg any
	if e.Message != "" {
		msg = e.Message
	}
	return fmt.Sprintf("details=%s code=%s message=%s", schema.PyRepr(details), schema.PyRepr(code), schema.PyRepr(msg))
}

// jobErrorDict is the Python SDK's JobError.to_json_dict() (None fields dropped, declaration order).
func jobErrorDict(e *genai.JobError) *schema.Map {
	m := schema.NewMap()
	if e.Details != nil {
		d := make([]any, len(e.Details))
		for i, s := range e.Details {
			d[i] = s
		}
		m.Set("details", d)
	}
	if e.Code != nil {
		m.Set("code", int64(*e.Code))
	}
	if e.Message != "" {
		m.Set("message", e.Message)
	}
	return m
}

// Fetch reads the results of an ended job, from its result file or its inlined responses.
func (g *Gemini) Fetch(ctx context.Context, batchID string) ([]Result, error) {
	c, err := g.getClient(ctx)
	if err != nil {
		return nil, err
	}
	job, err := c.Batches.Get(ctx, batchID, nil)
	if err != nil {
		return nil, err
	}
	var lines []*schema.Map
	switch {
	case job.Dest != nil && job.Dest.FileName != "":
		raw, err := c.Files.Download(ctx, genai.NewDownloadURIFromFile(&genai.File{DownloadURI: job.Dest.FileName}), nil)
		if err != nil {
			return nil, err
		}
		if lines, err = parseJSONLLines(string(raw)); err != nil {
			return nil, err
		}
	case job.Dest != nil && len(job.Dest.InlinedResponses) > 0:
		for _, r := range job.Dest.InlinedResponses {
			ln, err := inlinedLine(r)
			if err != nil {
				return nil, err
			}
			lines = append(lines, ln)
		}
	default:
		return nil, nil
	}
	out := make([]Result, 0, len(lines))
	for _, ln := range lines {
		out = append(out, geminiResult(ln))
	}
	return out, nil
}

// parseJSONLLines decodes the non-blank lines of a result file, keeping key order (for error reprs).
func parseJSONLLines(raw string) ([]*schema.Map, error) {
	var lines []*schema.Map
	for _, l := range splitLines(raw) {
		if strings.TrimFunc(l, unicode.IsSpace) == "" {
			continue
		}
		v, err := schema.LoadsOrdered(l)
		if err != nil {
			return nil, err
		}
		m, ok := v.(*schema.Map)
		if !ok {
			return nil, &schema.PyError{Type: "AttributeError", Msg: fmt.Sprintf("'%s' object has no attribute 'get'", schema.PyTypeName(v))}
		}
		lines = append(lines, m)
	}
	return lines, nil
}

// splitLines is str.splitlines() for the separators JSONL can contain.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// inlinedLine turns an inlined response into the dict Python built from it: {key, response, error}, with the
// response as the Python SDK's to_json_dict() (snake_case keys).
func inlinedLine(r *genai.InlinedResponse) (*schema.Map, error) {
	var key any
	if len(r.Metadata) > 0 {
		if k, ok := r.Metadata["key"]; ok {
			key = k
		}
	}
	var resp, errDict any
	if r.Response != nil {
		b, err := json.Marshal(r.Response)
		if err != nil {
			return nil, err
		}
		v, err := schema.LoadsOrdered(string(b))
		if err != nil {
			return nil, err
		}
		resp = snakeKeys(v)
	}
	if r.Error != nil {
		errDict = jobErrorDict(r.Error)
	}
	return schema.NewMap("key", key, "response", resp, "error", errDict), nil
}

// snakeKeys renames camelCase object keys to snake_case, recursively.
func snakeKeys(v any) any {
	switch x := v.(type) {
	case *schema.Map:
		out := schema.NewMap()
		for _, k := range x.Keys() {
			out.Set(snake(k), snakeKeys(x.Get(k)))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = snakeKeys(e)
		}
		return out
	}
	return v
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// geminiResult is Python's handling of one result line.
func geminiResult(ln *schema.Map) Result {
	key := ""
	if k := ln.Get("key"); k != nil {
		key = schema.PyStr(k)
	}
	if e := ln.Get("error"); pj.Truthy(schema.Plain(e)) {
		return Result{Key: key, Error: "gemini: " + schema.PyStr(e)}
	}
	resp, _ := ln.Get("response").(*schema.Map)
	if !pj.Truthy(schema.Plain(ln.Get("response"))) {
		resp = schema.NewMap()
	}
	usage := resp.Get("usageMetadata")
	if !pj.Truthy(schema.Plain(usage)) {
		usage = resp.Get("usage_metadata")
	}
	um, _ := usage.(*schema.Map)
	data, err := geminiAnswer(resp)
	if err != nil {
		var fr any
		if cands, ok := resp.Get("candidates").([]any); ok && len(cands) > 0 {
			if c0, ok := cands[0].(*schema.Map); ok {
				fr = c0.Get("finishReason")
			}
		}
		return Result{Key: key, Error: fmt.Sprintf("gemini parse: %v (finish=%s)", err, schema.PyStr(fr))}
	}
	pick := func(camel, snakeK string) any {
		if um.Has(camel) {
			return jsonFloat(um.Get(camel))
		}
		return jsonFloat(um.Get(snakeK))
	}
	return Result{Key: key, Data: data, Usage: pj.Obj{
		"in":      pick("promptTokenCount", "prompt_token_count"),
		"out":     pick("candidatesTokenCount", "candidates_token_count"),
		"thought": pick("thoughtsTokenCount", "thoughts_token_count"),
	}}
}

// jsonFloat turns a json.Number into float64 (pj's number type).
func jsonFloat(v any) any {
	if n, ok := v.(json.Number); ok {
		f, _ := pj.Float(n)
		return f
	}
	return v
}

// geminiAnswer is resp["candidates"][0]["content"]["parts"], joined text of the non-thought parts, validated —
// failing with the Python exception text where it would have failed.
func geminiAnswer(resp *schema.Map) (pj.Obj, error) {
	cands, err := subscript(resp, "candidates")
	if err != nil {
		return nil, err
	}
	c0, err := subscript(cands, 0)
	if err != nil {
		return nil, err
	}
	content, err := subscript(c0, "content")
	if err != nil {
		return nil, err
	}
	partsV, err := subscript(content, "parts")
	if err != nil {
		return nil, err
	}
	parts, ok := partsV.([]any)
	if !ok {
		return nil, &schema.PyError{Type: "TypeError", Msg: fmt.Sprintf("'%s' object is not iterable", schema.PyTypeName(partsV))}
	}
	var b strings.Builder
	for _, p := range parts {
		pm, ok := p.(*schema.Map)
		if !ok {
			return nil, &schema.PyError{Type: "AttributeError", Msg: fmt.Sprintf("'%s' object has no attribute 'get'", schema.PyTypeName(p))}
		}
		if pj.Truthy(schema.Plain(pm.Get("thought"))) {
			continue
		}
		t := pm.Get("text")
		if !pm.Has("text") {
			t = ""
		}
		s, ok := t.(string)
		if !ok {
			return nil, &schema.PyError{Type: "TypeError", Msg: fmt.Sprintf("sequence item 0: expected str instance, %s found", schema.PyTypeName(t))}
		}
		b.WriteString(s)
	}
	return schema.ParseAnswer(b.String())
}

// subscript is Python's v[k] on decoded JSON, with Python's exceptions.
func subscript(v any, k any) (any, error) {
	switch x := v.(type) {
	case *schema.Map:
		ks, ok := k.(string)
		if !ok || !x.Has(ks) {
			return nil, &schema.PyError{Type: "KeyError", Msg: schema.PyRepr(k)}
		}
		return x.Get(ks), nil
	case []any:
		i, ok := k.(int)
		if !ok {
			return nil, &schema.PyError{Type: "TypeError", Msg: "list indices must be integers or slices, not str"}
		}
		if i >= len(x) {
			return nil, &schema.PyError{Type: "IndexError", Msg: "list index out of range"}
		}
		return x[i], nil
	case string:
		if _, ok := k.(string); ok {
			return nil, &schema.PyError{Type: "TypeError", Msg: "string indices must be integers"}
		}
	}
	return nil, &schema.PyError{Type: "TypeError", Msg: fmt.Sprintf("'%s' object is not subscriptable", schema.PyTypeName(v))}
}

// Estimate is the cloud estimate with Gemini's image token rules.
func (g *Gemini) Estimate(model string, nWithCrop, nWithout int, cfg pj.Obj) Estimate {
	return batchEstimate(model, nWithCrop, nWithout, cfg)
}

// Classify is not available: Gemini is batch-only.
func (g *Gemini) Classify(_ context.Context, item Item, _ string, _ pj.Obj) Result {
	return errBatchOnly(g.Name(), item.Key)
}
