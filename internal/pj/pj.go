// Package pj works with the JSON documents photosort stores (local_json, vlm_json, override_json, the config) the
// way the Python code it replaces did: as dynamic maps, with Python's truthiness, rounding and number formatting.
// The stored shapes grew over years of rows (old rows lack keys new ones have), so a typed struct would drop or
// invent fields on a round trip; these helpers keep what they don't touch.
package pj

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

// Obj is a JSON object.
type Obj = map[string]any

// Get walks nested objects by key; nil when any step is missing or not an object.
func Get(v any, keys ...string) any {
	for _, k := range keys {
		o, ok := v.(Obj)
		if !ok {
			return nil
		}
		v = o[k]
	}
	return v
}

// O is Get as an object, nil when absent or not an object.
func O(v any, keys ...string) Obj {
	o, _ := Get(v, keys...).(Obj)
	return o
}

// A is Get as an array, nil when absent or not an array.
func A(v any, keys ...string) []any {
	a, _ := Get(v, keys...).([]any)
	return a
}

// Float converts a JSON number (float64 after decoding, or a Go numeric type set by code) to float64.
func Float(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case bool:
		// Python: True == 1 in arithmetic; stored JSON never relies on it, but config values might.
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// F is Float with 0 for anything that isn't a number.
func F(v any) float64 {
	f, _ := Float(v)
	return f
}

// Num reads a nested number: (value, present-and-numeric).
func Num(v any, keys ...string) (float64, bool) {
	return Float(Get(v, keys...))
}

// Int reads a number as an int (truncating), 0 when absent.
func Int(v any) int {
	f, _ := Float(v)
	return int(f)
}

// Str is a string value, "" otherwise.
func Str(v any) string {
	s, _ := v.(string)
	return s
}

// Truthy is Python's bool(): None, False, 0, "", [] and {} are false.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case Obj:
		return len(x) > 0
	}
	if f, ok := Float(v); ok {
		return f != 0
	}
	return true
}

// Or is Python's `a or b`.
func Or(a, b any) any {
	if Truthy(a) {
		return a
	}
	return b
}

// Round is Python's round(x, n) for n >= 0: correctly rounded from the exact binary value, ties to even. strconv
// does exactly that, so format and parse back.
func Round(x float64, n int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	if f == 0 {
		return 0 // no negative zero
	}
	return f
}

// RoundInt is Python's round(x) (to an int, ties to even).
func RoundInt(x float64) int {
	return int(math.RoundToEven(x))
}

// Sig is float(f"{x:.{n}g}"): x to n significant figures.
func Sig(x float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'g', n, 64), 64)
	return f
}

// RoundPtr rounds an optional value (Python's `None if v is None else round(v, n)`), passing nil through.
func RoundPtr(v any, n int) any {
	f, ok := Float(v)
	if !ok {
		return nil
	}
	return Round(f, n)
}

// Clone deep-copies a JSON value.
func Clone[T any](v T) T {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

// Parse decodes a JSON column; nil for NULL/empty text or invalid JSON.
func Parse(s *string) Obj {
	if s == nil || *s == "" {
		return nil
	}
	var o Obj
	if json.Unmarshal([]byte(*s), &o) != nil {
		return nil
	}
	return o
}

// ParseAny decodes any JSON value, nil on error.
func ParseAny(b []byte) any {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return v
}

// Dumps encodes like Python's json.dumps as far as it matters: no HTML escaping, no trailing newline.
func Dumps(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}

// DumpsPtr is Dumps for a nullable column: nil when v is falsy (Python's `json.dumps(d) if d else None`).
func DumpsPtr(v any) *string {
	if !Truthy(v) {
		return nil
	}
	s := Dumps(v)
	return &s
}

// Equal compares two JSON values structurally (numbers by value).
func Equal(a, b any) bool {
	return Dumps(normalize(a)) == Dumps(normalize(b))
}

func normalize(v any) any {
	// A round trip turns every number into float64 and every map into Obj, so Go-built and decoded values compare.
	return ParseAny([]byte(Dumps(v)))
}
