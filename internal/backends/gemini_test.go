package backends

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/mononendev/photosort/internal/pj"
)

func TestGeminiWriteJSONL(t *testing.T) {
	g := loadGolden(t)
	item, noCrop, cfg := goldenItems(t, g)
	path := filepath.Join(t.TempDir(), "dry-run.jsonl")
	if err := NewGemini(nil).WriteJSONL([]Item{item, noCrop}, "gemini-2.5-flash", cfg, path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(string(b), "\n")
	if len(lines) != 3 || lines[2] != "" || lines[0] != g.Requests.Payloads["gemini_2"] || !strings.HasPrefix(lines[1], `{"key": "43"`) {
		t.Errorf("jsonl: %d lines, first matches Python: %v", len(lines), lines[0] == g.Requests.Payloads["gemini_2"])
	}
}

// Python's json.dumps of result lines; the expected results below are what Python's fetch() made of them.
const geminiResultsJSONL = `{"key": "1", "response": {"candidates": [{"content": {"parts": [{"text": "thinking...", "thought": true}, {"text": "{\"focus_tier\": 2, \"focus_notes\": \"n\", \"p"}, {"text": "rimary_subject\": \"rider_action\", \"people_count\": 1, \"composition\": \"full_body\", \"subject_placement\": \"center\", \"action\": \"a\", \"keywords\": [\"B\", \"a\"], \"adjectives\": [\"x\"], \"description\": \"d\", \"quality_remarks\": \"q\", \"quality_score\": 4, \"keeper\": true}"}]}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 1500, "candidatesTokenCount": 200, "thoughtsTokenCount": 50}}}
{"key": "2", "error": {"code": 3, "message": "it's \"bad\"", "details": ["x"]}}
{"key": "3", "response": {"candidates": [{"content": {"parts": [{"text": "{\"focus_tier\": 2, \"focus_notes\": \"n\", \"primary_sub"}]}, "finishReason": "MAX_TOKENS"}]}}
{"key": "4", "response": {}}
{"key": "5", "response": {"candidates": []}}
{"key": "6", "response": {"candidates": [{"finishReason": "SAFETY"}]}}
{"key": "7", "response": {"candidates": [{"content": {"parts": [{"text": "{\"focus_tier\": 2, \"focus_notes\": \"n\", \"primary_subject\": \"rider_action\", \"people_count\": 1, \"composition\": \"full_body\", \"subject_placement\": \"center\", \"action\": \"a\", \"keywords\": [\"B\", \"a\"], \"adjectives\": [\"x\"], \"description\": \"d\", \"quality_remarks\": \"q\", \"quality_score\": 4, \"keeper\": true}"}]}}], "usage_metadata": {"prompt_token_count": 10}}}
{"key": "8"}
{"key": 9, "response": {"candidates": [{"content": {"parts": [{"text": "{}"}]}}]}}

`

const geminiAnswerJSON = `{"focus_tier": 2, "focus_notes": "n", "primary_subject": "rider_action", "people_count": 1, "composition": "full_body", "subject_placement": "center", "action": "a", "keywords": ["a", "b"], "adjectives": ["x"], "description": "d", "quality_remarks": "q", "quality_score": 4, "keeper": true}`

func TestGeminiResultsMatchPython(t *testing.T) {
	lines, err := parseJSONLLines(geminiResultsJSONL)
	if err != nil {
		t.Fatal(err)
	}
	var got []Result
	for _, ln := range lines {
		got = append(got, geminiResult(ln))
	}
	missingAll := "ValueError: missing fields: ['focus_tier', 'focus_notes', 'primary_subject', 'people_count', 'composition', 'subject_placement', 'action', 'keywords', 'adjectives', 'description', 'quality_remarks', 'quality_score', 'keeper']"
	want := []struct {
		key, err string
		usage    pj.Obj
	}{
		{"1", "", pj.Obj{"in": 1500, "out": 200, "thought": 50}},
		{"2", `gemini: {'code': 3, 'message': 'it\'s "bad"', 'details': ['x']}`, nil},
		{"3", "gemini parse: JSONDecodeError: Unterminated string starting at: line 1 column 39 (char 38) (finish=MAX_TOKENS)", nil},
		{"4", "gemini parse: KeyError: 'candidates' (finish=None)", nil},
		{"5", "gemini parse: IndexError: list index out of range (finish=None)", nil},
		{"6", "gemini parse: KeyError: 'content' (finish=SAFETY)", nil},
		{"7", "", pj.Obj{"in": 10, "out": nil, "thought": nil}},
		{"8", "gemini parse: KeyError: 'candidates' (finish=None)", nil},
		{"9", "gemini parse: " + missingAll + " (finish=None)", nil},
	}
	checkResults(t, got, want)
}

func checkResults(t *testing.T, got []Result, want []struct {
	key, err string
	usage    pj.Obj
}) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d results, want %d", len(got), len(want))
	}
	for i, w := range want {
		r := got[i]
		if r.Key != w.key || r.Error != w.err {
			t.Errorf("%d: key %q error %q\nwant key %q error %q", i, r.Key, r.Error, w.key, w.err)
		}
		if w.err == "" {
			if !pj.Equal(r.Data, pj.ParseAny([]byte(geminiAnswerJSON))) {
				t.Errorf("%d: data %v", i, r.Data)
			}
			if !pj.Equal(r.Usage, w.usage) {
				t.Errorf("%d: usage %v want %v", i, r.Usage, w.usage)
			}
		}
	}
}

func TestGeminiInlinedMatchPython(t *testing.T) {
	answer := func(text string, fr genai.FinishReason) *genai.GenerateContentResponse {
		return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
			Content: &genai.Content{Parts: []*genai.Part{{Text: text}}, Role: "model"}, FinishReason: fr}}}
	}
	ok := answer(geminiAnswerJSON, genai.FinishReasonStop)
	ok.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 8}
	code := int32(13)
	inl := []*genai.InlinedResponse{
		{Response: ok, Metadata: map[string]string{"key": "11"}},
		{Error: &genai.JobError{Code: &code, Message: "internal"}, Metadata: map[string]string{"key": "12"}},
		{Response: answer("{", genai.FinishReasonMaxTokens), Metadata: map[string]string{"key": "13"}},
	}
	var got []Result
	for _, r := range inl {
		ln, err := inlinedLine(r)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, geminiResult(ln))
	}
	checkResults(t, got, []struct {
		key, err string
		usage    pj.Obj
	}{
		{"11", "", pj.Obj{"in": 7, "out": 8, "thought": nil}},
		{"12", "gemini: {'code': 13, 'message': 'internal'}", nil},
		// Python read "finishReason" from a snake_case dict here, so it never saw the reason.
		{"13", "gemini parse: JSONDecodeError: Expecting property name enclosed in double quotes: line 1 column 2 (char 1) (finish=None)", nil},
	})
}

func TestGeminiStatus(t *testing.T) {
	code := int32(3)
	cases := []struct {
		job  genai.BatchJob
		want string
	}{
		{genai.BatchJob{State: genai.JobStateSucceeded}, "ended"},
		{genai.BatchJob{State: "JOB_STATE_PARTIALLY_SUCCEEDED"}, "ended"},
		{genai.BatchJob{State: genai.JobStateRunning}, "running"},
		{genai.BatchJob{State: genai.JobStatePending}, "running"},
		{genai.BatchJob{State: genai.JobStateFailed, Error: &genai.JobError{Code: &code, Message: "x"}}, "failed:JOB_STATE_FAILED:details=None code=3 message='x'"},
		{genai.BatchJob{State: genai.JobStateExpired, Error: &genai.JobError{Details: []string{"a"}, Message: "it's"}}, `failed:JOB_STATE_EXPIRED:details=['a'] code=None message="it's"`},
		{genai.BatchJob{State: genai.JobStateCancelled}, "failed:JOB_STATE_CANCELLED:None"},
	}
	for _, c := range cases {
		if got := geminiStatus(&c.job); got != c.want {
			t.Errorf("%s: %q want %q", c.job.State, got, c.want)
		}
	}
}
