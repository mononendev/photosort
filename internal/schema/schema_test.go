package schema

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
)

// golden is testdata/backends_golden.json, dumped from the Python code (see the script path in the backends
// package tests).
type golden struct {
	JSONSchema   map[string]string `json:"json_schema"`
	SystemPrompt string            `json:"system_prompt"`
	ContextText  []struct {
		Name, Local, Fixture, Text, Error string
	} `json:"context_text"`
	Validate []struct {
		Name, Input, Output, Error string
	} `json:"validate"`
	Loads []struct {
		Input, Output, Error string
	} `json:"loads"`
	FloatRepr []struct {
		Bits, Str string
	} `json:"float_repr"`
	StrRepr []struct {
		S, Repr string
	} `json:"str_repr"`
	Dumps []struct {
		Value json.RawMessage
		Text  string
	} `json:"dumps"`
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

func TestJSONSchemaMatchesPython(t *testing.T) {
	g := loadGolden(t)
	for _, strict := range []bool{false, true} {
		for _, ml := range []bool{false, true} {
			key := "strict=" + pyBool(strict) + ",max_lengths=" + pyBool(ml)
			want, ok := g.JSONSchema[key]
			if !ok {
				t.Fatalf("golden lacks %s", key)
			}
			if got := PyDumps(JSONSchema(strict, ml)); got != want {
				t.Errorf("%s:\n got %s\nwant %s", key, got, want)
			}
		}
	}
}

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// Port of tests/test_local.py::test_schema_validate_normalizes.
func TestSchemaValidateNormalizes(t *testing.T) {
	d := pj.Obj{}
	fields := Fields()
	for _, k := range FieldNames {
		d[k] = map[string]any{"integer": 1.0, "string": "x", "array": []any{"A", "a "}, "boolean": true}[fields.M(k).Get("type").(string)]
	}
	d["focus_tier"] = 2.0
	d["primary_subject"] = "nope"
	d["composition"] = "full_body"
	out, err := Validate(d)
	if err != nil {
		t.Fatal(err)
	}
	if out["primary_subject"] != "other" || !pj.Equal(out["keywords"], []any{"a"}) {
		t.Errorf("got %v", out)
	}
	if !JSONSchema(false, true).M("properties").M("quality_remarks").Has("maxLength") {
		t.Error("max_lengths schema lacks maxLength")
	}
	if JSONSchema(true, false).Get("additionalProperties") != false {
		t.Error("strict schema lacks additionalProperties=false")
	}
}

func TestSystemPromptMatchesPython(t *testing.T) {
	if g := loadGolden(t); SystemPrompt != g.SystemPrompt {
		t.Errorf("system prompt differs:\n got %q\nwant %q", SystemPrompt, g.SystemPrompt)
	}
}

func TestContextTextMatchesPython(t *testing.T) {
	g := loadGolden(t)
	if len(g.ContextText) < 20 {
		t.Fatalf("only %d context cases", len(g.ContextText))
	}
	for _, c := range g.ContextText {
		if c.Fixture != "" {
			c.Local = fixtureLocal(t, c.Fixture)
		}
		// Both decodings must give Python's text: json.Number (int/float per literal) and float64 (pj.Parse).
		num, err := Loads(c.Local)
		if err != nil {
			t.Fatal(err)
		}
		flt := pj.Parse(&c.Local)
		if c.Local == "{}" {
			flt = pj.Obj{}
		}
		for mode, local := range map[string]pj.Obj{"number": num.(pj.Obj), "float64": flt} {
			if mode == "float64" && c.Name == "int_sharp" {
				continue // Python ints in float fields: only the json.Number decoding can know (documented)
			}
			got, err := ContextText(local)
			if c.Error != "" {
				if err == nil || err.Error() != c.Error {
					t.Errorf("%s/%s: error %v, want %s", c.Name, mode, err, c.Error)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s/%s: %v", c.Name, mode, err)
				continue
			}
			if got != c.Text {
				t.Errorf("%s/%s:\n got %q\nwant %q", c.Name, mode, got, c.Text)
			}
		}
	}
	if s, _ := ContextText(nil); s != "Detector data unavailable." {
		t.Errorf("nil local: %q", s)
	}
}

// fixtureLocal is the "local" object of a testdata fixture, as JSON text.
func fixtureLocal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + path)
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Local json.RawMessage `json:"local"`
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	return string(fx.Local)
}

func TestValidateMatchesPython(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Validate {
		got, err := ParseAnswer(c.Input)
		if c.Error != "" {
			if err == nil || err.Error() != c.Error {
				t.Errorf("%s: error %v, want %s", c.Name, err, c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		want := pj.ParseAny([]byte(c.Output))
		if !pj.Equal(got, want) {
			t.Errorf("%s:\n got %s\nwant %s", c.Name, pj.Dumps(got), c.Output)
		}
		for k, v := range got {
			if _, ok := v.(json.Number); ok {
				t.Errorf("%s: %s left as json.Number", c.Name, k)
			}
		}
	}
}

func TestLoadsMatchesPython(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Loads {
		v, err := LoadsOrdered(c.Input)
		if c.Error != "" {
			if err == nil || err.Error() != c.Error {
				t.Errorf("%q: error %v, want %s", c.Input, err, c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.Input, err)
			continue
		}
		if got := PyDumps(v); got != c.Output {
			t.Errorf("%q: got %s want %s", c.Input, got, c.Output)
		}
	}
}

func TestPythonFormatting(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.FloatRepr {
		f, err := strconv.ParseFloat(pyHexToGo(c.Bits), 64)
		if err != nil {
			t.Fatalf("%s: %v", c.Bits, err)
		}
		if got := FloatRepr(f); got != c.Str {
			t.Errorf("FloatRepr(%v) = %s, want %s", f, got, c.Str)
		}
	}
	for _, c := range g.StrRepr {
		if got := PyRepr(c.S); got != c.Repr {
			t.Errorf("PyRepr(%q) = %s, want %s", c.S, got, c.Repr)
		}
	}
	for _, c := range g.Dumps {
		v, err := LoadsOrdered(string(c.Value))
		if err != nil {
			t.Fatal(err)
		}
		if got := PyDumps(v); got != c.Text {
			t.Errorf("PyDumps = %s, want %s", got, c.Text)
		}
	}
	if FloatRepr(math.Inf(-1)) != "-inf" || PyStr(nil) != "None" || PyStr(json.Number("-0")) != "0" || PyStr(json.Number("1E5")) != "100000.0" {
		t.Error("scalar str() mismatch")
	}
}

// pyHexToGo turns float.hex() output ("0x1.8000000000000p+1", "-0x0.0p+0") into a Go hex float literal.
func pyHexToGo(s string) string {
	switch s {
	case "0x0.0p+0":
		return "0"
	case "-0x0.0p+0":
		return "-0"
	}
	return strings.TrimSpace(s)
}

func TestMapOrderAndJSON(t *testing.T) {
	m := NewMap("b", 1, "a", NewMap("<", "x&y"))
	m.Set("b", 2).Set("c", []any{1.5})
	if b := pj.Dumps(m); b != `{"b":2,"a":{"<":"x&y"},"c":[1.5]}` {
		t.Errorf("marshal: %s", b)
	}
	if PyRepr(m) != `{'b': 2, 'a': {'<': 'x&y'}, 'c': [1.5]}` {
		t.Errorf("repr: %s", PyRepr(m))
	}
	if !pj.Equal(m.Obj(), pj.Obj{"b": 2, "a": pj.Obj{"<": "x&y"}, "c": []any{1.5}}) {
		t.Error("Obj")
	}
}
