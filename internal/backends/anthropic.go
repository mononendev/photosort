package backends

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/schema"
)

// Anthropic is the Anthropic Message Batches backend.
type Anthropic struct {
	opts   []option.RequestOption
	once   sync.Once
	client anthropic.Client
}

// NewAnthropic makes the backend; opts go to the SDK client (by default it reads ANTHROPIC_API_KEY from the
// environment, like Python's anthropic.Anthropic()). The client is created on first use.
func NewAnthropic(opts ...option.RequestOption) *Anthropic { return &Anthropic{opts: opts} }

func (a *Anthropic) Name() string         { return "anthropic" }
func (a *Anthropic) DefaultModel() string { return "claude-opus-5" }
func (a *Anthropic) Sync() bool           { return false }
func (a *Anthropic) MaxBatch() int        { return 350 } // 256 MB batch limit; ~0.6 MB per request after base64
func (a *Anthropic) BaseURL() string      { return "" }

func (a *Anthropic) getClient() *anthropic.Client {
	a.once.Do(func() { a.client = anthropic.NewClient(a.opts...) })
	return &a.client
}

// BuildRequest is the Messages API params for one item (Python's build_params): the stable system prompt first,
// marked cacheable so it is a shared prefix across the whole batch; the frame, the crop when there is one, and the
// detector text; strict JSON-schema output; and output effort (cfg anthropic_effort, default "low") except on
// Haiku, which does not accept it.
func (a *Anthropic) BuildRequest(item Item, model string, cfg pj.Obj) (any, error) {
	img := func(b []byte) *schema.Map {
		return schema.NewMap("type", "image", "source", schema.NewMap("type", "base64", "media_type", "image/jpeg",
			"data", base64.StdEncoding.EncodeToString(b)))
	}
	text := func(s string) *schema.Map { return schema.NewMap("type", "text", "text", s) }
	content := []any{text(frameLabel), img(item.Frame)}
	if len(item.Crop) > 0 {
		content = append(content, text(cropLabel), img(item.Crop))
	}
	content = append(content, text(userText(item)))
	maxTokens, err := cfgNeed(cfg, "max_output_tokens")
	if err != nil {
		return nil, err
	}
	outCfg := schema.NewMap("format", schema.NewMap("type", "json_schema", "schema", schema.JSONSchema(true, false)))
	if !strings.Contains(model, "haiku") { // effort is not accepted on Haiku 4.5
		outCfg.Set("effort", cfgGet(cfg, "anthropic_effort", "low"))
	}
	return schema.NewMap(
		"model", model,
		"max_tokens", maxTokens,
		"system", []any{schema.NewMap("type", "text", "text", schema.SystemPrompt, "cache_control", schema.NewMap("type", "ephemeral"))},
		"messages", []any{schema.NewMap("role", "user", "content", content)},
		"output_config", outCfg,
	), nil
}

// Submit creates one message batch with a request per item (custom_id = item key) and returns its id.
//
// The params are sent exactly as BuildRequest builds them (param.Override with the ordered JSON), so the body is
// the one the Python SDK sent, independent of which fields the Go SDK types.
func (a *Anthropic) Submit(ctx context.Context, items []Item, model string, cfg pj.Obj, _ string) (string, error) {
	reqs := make([]anthropic.MessageBatchNewParamsRequest, 0, len(items))
	for _, it := range items {
		p, err := a.BuildRequest(it, model, cfg)
		if err != nil {
			return "", err
		}
		reqs = append(reqs, anthropic.MessageBatchNewParamsRequest{
			CustomID: it.Key,
			Params:   param.Override[anthropic.MessageBatchNewParamsRequestParams](p),
		})
	}
	b, err := a.getClient().Messages.Batches.New(ctx, anthropic.MessageBatchNewParams{Requests: reqs})
	if err != nil {
		return "", err
	}
	return b.ID, nil
}

// Status is "ended" once the batch has finished processing, else "running".
func (a *Anthropic) Status(ctx context.Context, batchID string) (string, error) {
	b, err := a.getClient().Messages.Batches.Get(ctx, batchID, anthropic.MessageBatchGetParams{})
	if err != nil {
		return "", err
	}
	if b.ProcessingStatus == "ended" {
		return "ended", nil
	}
	return "running", nil
}

// Fetch streams the batch results. Non-succeeded results and refusals become errors; usage includes prompt-cache
// reads and writes.
func (a *Anthropic) Fetch(ctx context.Context, batchID string) ([]Result, error) {
	stream := a.getClient().Messages.Batches.ResultsStreaming(ctx, batchID, anthropic.MessageBatchResultsParams{})
	defer stream.Close()
	var out []Result
	for stream.Next() {
		out = append(out, anthropicResult(stream.Current()))
	}
	if err := stream.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// anthropicResult is Python's handling of one batch result, error strings included.
func anthropicResult(r anthropic.MessageBatchIndividualResponse) Result {
	key := r.CustomID
	t := r.Result.Type
	if t != "succeeded" {
		errStr := "None"
		if t == "errored" {
			errStr = errorResponseRepr(r.Result.Error.Error.Type, r.Result.Error.Error.Message,
				r.Result.Error.RequestID, r.Result.Error.JSON.RequestID.Valid())
		}
		return Result{Key: key, Error: fmt.Sprintf("anthropic %s: %s", t, errStr)}
	}
	msg := r.Result.Message
	if msg.StopReason == "refusal" {
		details := "None"
		if msg.JSON.StopDetails.Valid() {
			sd := msg.StopDetails
			opt := func(s string, valid bool) any {
				if !valid {
					return nil
				}
				return s
			}
			details = fmt.Sprintf("RefusalStopDetails(category=%s, explanation=%s, type=%s)",
				schema.PyRepr(opt(string(sd.Category), sd.JSON.Category.Valid())),
				schema.PyRepr(opt(sd.Explanation, sd.JSON.Explanation.Valid())), schema.PyRepr(string(sd.Type)))
		}
		return Result{Key: key, Error: "anthropic refusal: " + details}
	}
	stop := "None"
	if msg.JSON.StopReason.Valid() {
		stop = string(msg.StopReason)
	}
	text, found := "", false
	for _, b := range msg.Content {
		if b.Type == "text" {
			text, found = b.Text, true
			break
		}
	}
	if !found {
		// Python: next() on an exhausted generator; str(StopIteration()) is empty.
		return Result{Key: key, Error: fmt.Sprintf("anthropic parse: StopIteration:  (stop=%s)", stop)}
	}
	data, err := schema.ParseAnswer(text)
	if err != nil {
		return Result{Key: key, Error: fmt.Sprintf("anthropic parse: %v (stop=%s)", err, stop)}
	}
	u := msg.Usage
	opt := func(v int64, valid bool) any {
		if !valid {
			return nil
		}
		return float64(v)
	}
	return Result{Key: key, Data: data, Usage: pj.Obj{
		"in": float64(u.InputTokens), "out": float64(u.OutputTokens),
		"cache_read":  opt(u.CacheReadInputTokens, u.JSON.CacheReadInputTokens.Valid()),
		"cache_write": opt(u.CacheCreationInputTokens, u.JSON.CacheCreationInputTokens.Valid()),
	}}
}

// errorClasses are the Python SDK's class names for each error type (str() of its ErrorResponse shows them).
var errorClasses = map[string]string{
	"invalid_request_error": "InvalidRequestError",
	"authentication_error":  "AuthenticationError",
	"billing_error":         "BillingError",
	"permission_error":      "PermissionError",
	"not_found_error":       "NotFoundError",
	"rate_limit_error":      "RateLimitError",
	"timeout_error":         "GatewayTimeoutError",
	"api_error":             "APIErrorObject",
	"overloaded_error":      "OverloadedError",
}

// errorResponseRepr is str() of the Python SDK's ErrorResponse.
func errorResponseRepr(typ, message, requestID string, hasRequestID bool) string {
	cls, ok := errorClasses[typ]
	if !ok {
		cls = "ErrorObject"
	}
	var rid any
	if hasRequestID {
		rid = requestID
	}
	return fmt.Sprintf("ErrorResponse(error=%s(message=%s, type=%s), request_id=%s, type='error')",
		cls, schema.PyRepr(message), schema.PyRepr(typ), schema.PyRepr(rid))
}

// Estimate is the cloud estimate with Claude's image token rules.
func (a *Anthropic) Estimate(model string, nWithCrop, nWithout int, cfg pj.Obj) Estimate {
	return batchEstimate(model, nWithCrop, nWithout, cfg)
}

// Classify is not available: Anthropic is batch-only here.
func (a *Anthropic) Classify(_ context.Context, item Item, _ string, _ pj.Obj) Result {
	return errBatchOnly(a.Name(), item.Key)
}
