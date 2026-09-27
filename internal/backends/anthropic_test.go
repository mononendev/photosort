package backends

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/mononendev/photosort/internal/pj"
	"github.com/mononendev/photosort/internal/py"
)

func msgJSON(stop, content, extra string) string {
	return `{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5", "content": ` + content +
		`, "stop_reason": ` + stop + `, "stop_sequence": null, "usage": {"input_tokens": 1500, "output_tokens": 300, "cache_read_input_tokens": 1200}` + extra + `}`
}

func succeeded(id, msg string) string {
	return `{"custom_id": "` + id + `", "result": {"type": "succeeded", "message": ` + msg + `}}`
}

func TestAnthropicBatchRoundTrip(t *testing.T) {
	g := loadGolden(t)
	item, _, cfg := goldenItems(t, g)
	answer := strings.ReplaceAll(strings.ReplaceAll(geminiAnswerJSON, `\`, `\\`), `"`, `\"`)
	results := strings.Join([]string{
		succeeded("1", msgJSON(`"end_turn"`, `[{"type": "text", "text": "`+answer+`"}]`, "")),
		`{"custom_id": "2", "result": {"type": "errored", "error": {"type": "error", "error": {"type": "invalid_request_error", "message": "bad thing"}, "request_id": "req_1"}}}`,
		`{"custom_id": "3", "result": {"type": "canceled"}}`,
		`{"custom_id": "4", "result": {"type": "expired"}}`,
		succeeded("5", msgJSON(`"refusal"`, `[]`, `, "stop_details": {"type": "refusal", "category": "cyber", "explanation": "no"}`)),
		succeeded("6", msgJSON(`"refusal"`, `[]`, "")),
		succeeded("7", msgJSON(`"end_turn"`, `[{"type": "thinking", "thinking": "x", "signature": "s"}]`, "")),
		succeeded("8", msgJSON(`"max_tokens"`, `[{"type": "text", "text": ""}]`, "")),
	}, "\n")

	var submitted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/batches":
			b, _ := io.ReadAll(r.Body)
			submitted = string(b)
			_, _ = io.WriteString(w, `{"id": "msgbatch_1", "type": "message_batch", "processing_status": "in_progress"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/batches/msgbatch_1":
			_, _ = io.WriteString(w, `{"id": "msgbatch_1", "type": "message_batch", "processing_status": "ended"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/batches/msgbatch_1/results":
			w.Header().Set("Content-Type", "application/x-jsonl")
			_, _ = io.WriteString(w, results+"\n")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	a := NewAnthropic(option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
	ctx := context.Background()

	id, err := a.Submit(ctx, []Item{item}, "claude-opus-5", cfg, "")
	if err != nil || id != "msgbatch_1" {
		t.Fatalf("submit: %q %v", id, err)
	}
	// The params go out exactly as Python's build_params: same keys, same order.
	body, err := py.LoadsNumberOrdered(submitted)
	if err != nil {
		t.Fatal(err)
	}
	req := body.(*py.Object).Get("requests").([]any)[0].(*py.Object)
	if req.Get("custom_id") != "42" {
		t.Errorf("custom_id %v", req.Get("custom_id"))
	}
	if got := py.Dumps(req.Get("params")); got != g.Requests.Payloads["anthropic_opus"] {
		t.Errorf("submitted params differ from Python's:\n%s", firstDiff(got, g.Requests.Payloads["anthropic_opus"]))
	}

	if st, err := a.Status(ctx, "msgbatch_1"); err != nil || st != "ended" {
		t.Errorf("status %q %v", st, err)
	}
	got, err := a.Fetch(ctx, "msgbatch_1")
	if err != nil {
		t.Fatal(err)
	}
	checkResults(t, got, []struct {
		key, err string
		usage    pj.Obj
	}{
		{"1", "", pj.Obj{"in": 1500, "out": 300, "cache_read": 1200, "cache_write": nil}},
		{"2", "anthropic errored: ErrorResponse(error=InvalidRequestError(message='bad thing', type='invalid_request_error'), request_id='req_1', type='error')", nil},
		{"3", "anthropic canceled: None", nil},
		{"4", "anthropic expired: None", nil},
		{"5", "anthropic refusal: RefusalStopDetails(category='cyber', explanation='no', type='refusal')", nil},
		{"6", "anthropic refusal: None", nil},
		{"7", "anthropic parse: StopIteration:  (stop=end_turn)", nil},
		{"8", "anthropic parse: JSONDecodeError: Expecting value: line 1 column 1 (char 0) (stop=max_tokens)", nil},
	})
	if r := a.Classify(ctx, item, "claude-opus-5", cfg); r.Error == "" {
		t.Error("classify on a batch backend should fail")
	}
}
