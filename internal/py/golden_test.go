package py

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/mononendev/photosort/internal/pj"
)

// golden is the json.loads/json.dumps/repr part of testdata/backends_golden.json, dumped from CPython (see the
// script path in the backends package tests).
type golden struct {
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

func TestLoadsMatchesPython(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Loads {
		v, err := LoadsNumberOrdered(c.Input)
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
		if got := Dumps(v); got != c.Output {
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
		if got := Repr(c.S); got != c.Repr {
			t.Errorf("Repr(%q) = %s, want %s", c.S, got, c.Repr)
		}
	}
	for _, c := range g.Dumps {
		v, err := LoadsNumberOrdered(string(c.Value))
		if err != nil {
			t.Fatal(err)
		}
		if got := Dumps(v); got != c.Text {
			t.Errorf("Dumps = %s, want %s", got, c.Text)
		}
	}
	if FloatRepr(math.Inf(-1)) != "-inf" || Str(nil) != "None" || Str(json.Number("-0")) != "0" || Str(json.Number("1E5")) != "100000.0" {
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

func TestObjectOrderAndJSON(t *testing.T) {
	m := NewObject("b", 1, "a", NewObject("<", "x&y"))
	m.Set("b", 2).Set("c", []any{1.5})
	if b := pj.Dumps(m); b != `{"b":2,"a":{"<":"x&y"},"c":[1.5]}` {
		t.Errorf("marshal: %s", b)
	}
	if Repr(m) != `{'b': 2, 'a': {'<': 'x&y'}, 'c': [1.5]}` {
		t.Errorf("repr: %s", Repr(m))
	}
	if !pj.Equal(m.Obj(), pj.Obj{"b": 2, "a": pj.Obj{"<": "x&y"}, "c": []any{1.5}}) {
		t.Error("Obj")
	}
}
