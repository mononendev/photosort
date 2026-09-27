// Package schema is the output contract for the vision model, shared by all backends: the JSON schema the model
// must answer with, the system prompt, the per-image detector summary sent with each photo, and the light
// normalization applied to every answer.
//
// It is a port of photosort/schema.py and must stay behaviour-identical: the schema (including key order), the
// prompt and the context text are sent to model APIs, and the answers are validated the same way. The package also
// carries the small Python-compatibility layer that needs (Map, PyDumps, PyStr/PyRepr, Loads, PyError).
package schema

import (
	"encoding/json"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/mononendev/photosort/internal/pj"
)

// Enumerations the model must pick from.
var (
	Subjects     = []string{"rider_action", "rider_posed", "group", "crowd_spectators", "gear_board", "venue_scenery", "other", "no_people"}
	Compositions = []string{"full_body", "three_quarter", "half_body", "close_up", "environmental", "no_subject"}
	Placements   = []string{"center", "left_third", "right_third", "top", "bottom", "edge", "none"}
)

// FieldNames are the answer's fields in schema order (the order of `properties` and `required`).
var FieldNames = []string{
	"focus_tier", "focus_notes", "primary_subject", "people_count", "composition", "subject_placement", "action",
	"keywords", "adjectives", "description", "quality_remarks", "quality_score", "keeper",
}

// MaxLengths are the string caps json_schema(max_lengths=True) adds, in Python's dict order.
var MaxLengths = []struct {
	Field string
	N     int
}{{"focus_notes", 220}, {"action", 60}, {"description", 140}, {"quality_remarks", 260}}

func strs(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// Fields returns a fresh copy of the per-field schema (Python's FIELDS), keyed in FieldNames order.
func Fields() *Map {
	return NewMap(
		"focus_tier", NewMap("type", "integer", "enum", []any{0, 1, 2, 3},
			"description", "0 = missed, primary person's head not in focus; 1 = soft, head clearly soft but recognizable; 2 = slightly soft, head nearly but not crisply in focus; 3 = sharp, primary person's head/eyes/helmet edges crisp"),
		"focus_notes", NewMap("type", "string", "description", "What is and isn't sharp; where focus landed; motion blur vs missed focus. 1-2 sentences."),
		"primary_subject", NewMap("type", "string", "enum", strs(Subjects)),
		"people_count", NewMap("type", "integer", "description", "Number of clearly visible people, 0 if none."),
		"composition", NewMap("type", "string", "enum", strs(Compositions),
			"description", "Portion of the primary subject in frame. environmental = subject small in a big scene."),
		"subject_placement", NewMap("type", "string", "enum", strs(Placements)),
		"action", NewMap("type", "string", "description", "Short phrase for what the primary subject is doing, e.g. 'carving a berm', 'mid-air jump', 'standing and talking', or 'none'."),
		"keywords", NewMap("type", "array", "items", NewMap("type", "string"), "minItems", 5, "maxItems", 15,
			"description", "Lowercase nouns/phrases for what is in the photo: objects, setting, gear, tricks, weather, time of day."),
		"adjectives", NewMap("type", "array", "items", NewMap("type", "string"), "minItems", 3, "maxItems", 8,
			"description", "Mood, style and light descriptors, e.g. 'dynamic', 'golden-hour', 'gritty'."),
		"description", NewMap("type", "string", "description", "One-sentence caption."),
		"quality_remarks", NewMap("type", "string", "description", "1-3 sentences a photo editor would write: exposure, motion blur, noise, clipped highlights, distracting elements, horizon, crop suggestion."),
		"quality_score", NewMap("type", "integer", "minimum", 1, "maximum", 5, "description", "Overall keeper quality, 1 worst to 5 best."),
		"keeper", NewMap("type", "boolean", "description", "Would a photographer keep this in the delivered set?"),
	)
}

// JSONSchema is the answer's JSON schema. strict adds additionalProperties=false (Anthropic's strict structured
// output needs it); maxLengths adds string caps (used with grammar-constrained local models to stop runaway text).
// The result marshals to the same JSON, in the same key order, as Python's json_schema().
func JSONSchema(strict, maxLengths bool) *Map {
	props := Fields()
	if maxLengths {
		for _, ml := range MaxLengths {
			props.M(ml.Field).Set("maxLength", ml.N)
		}
		props.M("keywords").Set("items", NewMap("type", "string", "maxLength", 40))
		props.M("adjectives").Set("items", NewMap("type", "string", "maxLength", 30))
	}
	s := NewMap("type", "object", "properties", props, "required", strs(FieldNames))
	if strict {
		s.Set("additionalProperties", false)
	}
	return s
}

// SystemPrompt is the stable system prompt, identical for every image (and so a cacheable prefix).
const SystemPrompt = `You are assisting an action-sports photographer who shoots onewheel (self-balancing electric board) events with fast lenses near wide open, so depth of field is shallow and focus can vary across a single person's body.

For each photo you get:
- Image 1: the full frame, downscaled.
- Image 2 (when present): a crop at native pixel resolution around the primary person's head and upper body, chosen by a person detector. Judge fine focus from this crop; the downscaled frame cannot show it.
- Detector data: people count, primary subject size and position, and sharpness numbers measured on the original pixels (contrast-normalized; higher is sharper; head/torso/body for the primary person, plus the background). Use them as evidence, not as the answer; the crop is what you can see.

Focus tiers:
Grade the primary person's head (eyes, face, or helmet edges):
- 3 sharp: crisply in focus.
- 2 slightly soft: nearly in focus but not crisp: just in front of or behind the eyes, or slight motion blur; fine detail is a little mushy at 100%.
- 1 soft: clearly soft: the face is recognizable but detail is gone, from missed focus or motion blur.
- 0 miss: the head is blurry: no people, focus landed on the background, foreground, torso or board, or on someone else.
Distinguish missed focus (whole subject soft while something else is crisp) from motion blur (directional smear) in focus_notes.

Composition is the portion of the primary subject in frame. Placement is where the subject sits in the frame.
Keywords are concrete lowercase library tags for what is in the photo. Adjectives describe mood, style and light. Quality remarks are an editor's cull notes about the photo itself: exposure, blur, noise, clipping, distractions, crop.
Write like a photo editor, in short plain sentences about the photo only: focus_notes at most 25 words, description at most 15 words, quality_remarks at most 35 words, 5-10 keywords, 3-5 adjectives. Return only the JSON object.`

// ParseAnswer decodes a model's JSON answer the way Python's json.loads did (see Loads) and validates it.
// Errors are *PyError and read like the Python ones ("JSONDecodeError: Expecting value: ...",
// "ValueError: missing fields: [...]").
func ParseAnswer(text string) (pj.Obj, error) {
	v, err := Loads(text)
	if err != nil {
		return nil, err
	}
	return Validate(v)
}

// Validate is the light normalization of a parsed answer; it fails on missing required fields.
//   - focus_tier is int()'d and clamped to 0..3;
//   - keywords and adjectives become sorted, de-duplicated, stripped, lowercased strings (empty ones dropped);
//   - an unknown primary_subject becomes "other", an unknown composition "no_subject".
//
// d is normally a pj.Obj from Loads; like Python it is modified in place and returned. Other JSON values fail the
// way Python's validate() did on them. Numbers are str()'d the Python way (json.Number keeps int vs float; a plain
// float64 counts as a float). json.Number values left in the result are converted to float64, so the rest of the Go
// code sees the same number types as from pj.Parse.
func Validate(d any) (pj.Obj, error) {
	if err := checkMissing(d); err != nil {
		return nil, err
	}
	o, ok := d.(pj.Obj)
	if !ok {
		// Python: d["focus_tier"] on a list or str.
		if _, isList := d.([]any); isList {
			return nil, pyErr("TypeError", "list indices must be integers or slices, not str")
		}
		return nil, pyErr("TypeError", "string indices must be integers")
	}
	tier, err := pyInt(o["focus_tier"])
	if err != nil {
		return nil, err
	}
	o["focus_tier"] = float64(max(0, min(3, tier)))
	for _, k := range []string{"keywords", "adjectives"} {
		tags, err := tagSet(o[k])
		if err != nil {
			return nil, err
		}
		o[k] = tags
	}
	if s, ok := o["primary_subject"].(string); !ok || !slices.Contains(Subjects, s) {
		o["primary_subject"] = "other"
	}
	if s, ok := o["composition"].(string); !ok || !slices.Contains(Compositions, s) {
		o["composition"] = "no_subject"
	}
	for k, v := range o {
		o[k] = floatNumbers(v)
	}
	return o, nil
}

// checkMissing is Python's `[k for k in FIELDS if k not in d]` for whatever d is.
func checkMissing(d any) error {
	var in func(k string) bool
	switch x := d.(type) {
	case pj.Obj:
		in = func(k string) bool { _, ok := x[k]; return ok }
	case []any:
		in = func(k string) bool {
			for _, e := range x {
				if s, ok := e.(string); ok && s == k {
					return true
				}
			}
			return false
		}
	case string:
		in = func(k string) bool { return strings.Contains(x, k) }
	default:
		return pyErr("TypeError", "argument of type '%s' is not iterable", PyTypeName(d))
	}
	var missing []string
	for _, k := range FieldNames {
		if !in(k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return pyErr("ValueError", "missing fields: %s", PyRepr(missing))
	}
	return nil
}

// pyInt is Python's int(v) for a JSON value; the result is clamped to the int range (only 0..3 matters).
func pyInt(v any) (int, error) {
	clampBig := func(b *big.Int) int {
		if b.IsInt64() {
			return int(max(math.MinInt32, min(math.MaxInt32, b.Int64())))
		}
		if b.Sign() < 0 {
			return math.MinInt32
		}
		return math.MaxInt32
	}
	fromFloat := func(f float64) (int, error) {
		switch {
		case math.IsNaN(f):
			return 0, pyErr("ValueError", "cannot convert float NaN to integer")
		case math.IsInf(f, 0):
			return 0, pyErr("OverflowError", "cannot convert float infinity to integer")
		}
		return int(max(math.MinInt32, min(math.MaxInt32, math.Trunc(f)))), nil
	}
	switch x := v.(type) {
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		if isIntLiteral(string(x)) {
			if b, ok := new(big.Int).SetString(string(x), 10); ok {
				return clampBig(b), nil
			}
		}
		f, _ := strconv.ParseFloat(string(x), 64)
		return fromFloat(f)
	case float64:
		return fromFloat(x)
	case int:
		return x, nil
	case int64:
		return int(max(math.MinInt32, min(math.MaxInt32, x))), nil
	case string:
		t := pyStrip(x)
		t2 := strings.TrimLeft(t, "+-")
		if len(t)-len(t2) <= 1 && validIntDigits(t2) {
			if b, ok := new(big.Int).SetString(strings.ReplaceAll(t, "_", ""), 10); ok {
				return clampBig(b), nil
			}
		}
		return 0, pyErr("ValueError", "invalid literal for int() with base 10: %s", PyRepr(x))
	}
	return 0, pyErr("TypeError", "int() argument must be a string, a bytes-like object or a real number, not '%s'", PyTypeName(v))
}

// validIntDigits: ASCII digits with single underscores only between digits (Python's int() literal rules).
func validIntDigits(s string) bool {
	if s == "" || s[0] == '_' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
		return false
	}
	for _, r := range s {
		if r != '_' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// tagSet is sorted({str(k).strip().lower() for k in v if str(k).strip()}).
func tagSet(v any) ([]any, error) {
	var items []any
	switch x := v.(type) {
	case []any:
		items = x
	case string:
		for _, r := range x {
			items = append(items, string(r))
		}
	case pj.Obj:
		for k := range x {
			items = append(items, k)
		}
	case *Map:
		for _, k := range x.Keys() {
			items = append(items, k)
		}
	default:
		return nil, pyErr("TypeError", "'%s' object is not iterable", PyTypeName(v))
	}
	set := map[string]bool{}
	for _, it := range items {
		if s := pyStrip(PyStr(it)); s != "" {
			set[pyLower(s)] = true
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys) // byte order of UTF-8 == code point order, as Python sorts str
	return strs(keys), nil
}

// floatNumbers replaces json.Number with float64 throughout v.
func floatNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := pj.Float(x)
		return f
	case []any:
		for i, e := range x {
			x[i] = floatNumbers(e)
		}
	case pj.Obj:
		for k, e := range x {
			x[k] = floatNumbers(e)
		}
	}
	return v
}
