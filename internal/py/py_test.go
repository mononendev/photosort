package py

import (
	"math"
	"testing"
)

// Expected values are what CPython 3.10 prints; the export/truth golden tests cover these helpers end to end.

func TestFloatRepr(t *testing.T) {
	for f, want := range map[float64]string{
		1: "1.0", 0.30000000000000004: "0.30000000000000004", 1e16: "1e+16", 1234567890123456: "1234567890123456.0",
		123456789012345678: "1.2345678901234568e+17", 1e-05: "1e-05", 0.0001: "0.0001", 1e22: "1e+22",
		5e-324: "5e-324", -2.5: "-2.5", 1.5e-7: "1.5e-07", 100: "100.0", math.Inf(1): "inf",
	} {
		if got := FloatRepr(f); got != want {
			t.Errorf("%v: %s, want %s", f, got, want)
		}
	}
	if got := FloatRepr(math.Copysign(0, -1)); got != "-0.0" {
		t.Error(got)
	}
}

func TestLoadsDumpsRoundTrip(t *testing.T) {
	for in, want := range map[string]string{
		`{"b":1,"a":[1,2.5,null,true,1.0,1e5,-0.0],"b":2}`:  `{"b": 2, "a": [1, 2.5, null, true, 1.0, 100000.0, -0.0]}`,
		`"\u00e9\ud83d\udeb2\u007f<&>/"`:                    `"\u00e9\ud83d\udeb2\u007f<&>/"`,
		"\"é🚲\"":                                            `"\u00e9\ud83d\udeb2"`,
		`[NaN, Infinity, -Infinity, 123456789012345678901]`: `[NaN, Infinity, -Infinity, 123456789012345678901]`,
		" {} ": `{}`, `"tab\tnl\n\"q\"\\"`: `"tab\tnl\n\"q\"\\"`,
	} {
		v, err := Loads(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := Dumps(v); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{`{"a" 1}`, `[1,]`, `01`, `1.`, `"\x"`, "\"a\nb\"", `{} x`, ``} {
		if _, err := Loads(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestReprAndStr(t *testing.T) {
	o := NewObject("k", []any{"it's", `say "hi"`, "both ' \"", int64(1), 2.0, nil, true, "é\x07\u2028"})
	if got, want := Repr(o), `{'k': ["it's", 'say "hi"', 'both \' "', 1, 2.0, None, True, 'é\x07\u2028']}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if Str("x") != "x" || Str(nil) != "None" || Str(false) != "False" {
		t.Error("str")
	}
}

func TestPaths(t *testing.T) {
	for _, c := range [][4]string{ // path, name, stem, suffix
		{"/a/b.tar.gz", "b.tar.gz", "b.tar", ".gz"}, {".hidden", ".hidden", ".hidden", ""}, {"a/b.", "b.", "b.", ""},
		{"../clients", "clients", "clients", ""}, {"sub/./", "sub", "sub", ""}, {"/", "", "", ""}, {"", "", "", ""},
	} {
		if Name(c[0]) != c[1] || Stem(c[0]) != c[2] || Suffix(c[0]) != c[3] {
			t.Errorf("%q: %q %q %q", c[0], Name(c[0]), Stem(c[0]), Suffix(c[0]))
		}
	}
	if got := WithSuffix("/x/IMG_1.CR2", ".xmp"); got != "/x/IMG_1.xmp" {
		t.Error(got)
	}
	if got := Join("out", "t", "../up", "/abs", "x"); got != "/abs/x" {
		t.Error(got)
	}
	if got := Join("out", "t", "../up"); got != "out/t/../up" {
		t.Error(got)
	}
}

func TestStringHelpers(t *testing.T) {
	if Lower("\u0130MG_ΣΑΣ.JPG \u03a3A\u03a3") != "i\u0307mg_σασ.jpg σaς" {
		t.Error("lower")
	}
	if Strip("\x1c\v a b\u3000") != "a b" {
		t.Error("strip")
	}
	if n, ok := Int(" -٣_4 "); !ok || n != -34 {
		t.Error(n, ok)
	}
	for _, bad := range []string{"--3", "²", "3_", "_3", ""} {
		if _, ok := Int(bad); ok {
			t.Error(bad)
		}
	}
	if !IsDigit("²3") || IsDigit("3a") || IsDigit("") {
		t.Error("isdigit")
	}
}

func TestCSV(t *testing.T) {
	recs, err := ReadCSV("a,\"b,\"\"c\"\"\"\r\n\n\"multi\nline\",x\"y\n\"q\"tail")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a", `b,"c"`}, nil, {"multi\nline", `x"y`}, {"qtail"}}
	if len(recs) != len(want) {
		t.Fatalf("%q", recs)
	}
	for i := range want {
		if len(recs[i]) != len(want[i]) {
			t.Fatalf("%d: %q", i, recs[i])
		}
		for j := range want[i] {
			if recs[i][j] != want[i][j] {
				t.Errorf("%d/%d: %q", i, j, recs[i][j])
			}
		}
	}
}
