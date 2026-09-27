package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mononendev/photosort/internal/pj"
)

// PyError is an error that reads the way the Python code reported it: callers there formatted exceptions as
// f"{type(e).__name__}: {e}", and those strings end up in the database and the UI. Error() returns exactly that.
type PyError struct {
	Type string // Python exception class name, e.g. "ValueError", "KeyError", "JSONDecodeError"
	Msg  string // str(e)
}

func (e *PyError) Error() string { return e.Type + ": " + e.Msg }

func pyErr(typ, format string, a ...any) *PyError {
	return &PyError{Type: typ, Msg: fmt.Sprintf(format, a...)}
}

// keyError is Python's KeyError for a missing dict key: str() of it is the key's repr.
func keyError(k string) *PyError { return &PyError{Type: "KeyError", Msg: PyRepr(k)} }

// PyTypeName is type(v).__name__ for a JSON value as this package represents them.
func PyTypeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if isIntLiteral(string(x)) {
			return "int"
		}
		return "float"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "float"
	case []any, []string:
		return "list"
	case pj.Obj, *Map:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

func isIntLiteral(s string) bool { return !strings.ContainsAny(s, ".eEnN") }

// FloatRepr is Python's repr()/str() of a float: the shortest string that round-trips, in fixed notation when the
// decimal exponent is in [-4, 16) and scientific otherwise ("1e-05", "1e+16"), always with a ".0" when integral.
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // "-1.2345e-05"
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	mant, expS, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expS)
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1 // position of the decimal point relative to the digit string
	if -4 < decpt && decpt <= 16 {
		switch {
		case decpt <= 0:
			return sign + "0." + strings.Repeat("0", -decpt) + digits
		case decpt >= len(digits):
			return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
		default:
			return sign + digits[:decpt] + "." + digits[decpt:]
		}
	}
	out := sign + digits[:1]
	if len(digits) > 1 {
		out += "." + digits[1:]
	}
	esign := "+"
	if exp < 0 {
		esign, exp = "-", -exp
	}
	return fmt.Sprintf("%se%s%02d", out, esign, exp)
}

// numberStr is str() of a JSON number literal as Python's json module would have parsed it: an int literal prints
// as the (normalized) integer, anything with a fraction or exponent as a float.
func numberStr(n json.Number) string {
	s := string(n)
	if isIntLiteral(s) {
		if b, ok := new(big.Int).SetString(s, 10); ok {
			return b.String()
		}
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// Out of range: Python's float() gives inf; ParseFloat returns ±Inf with the error.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return FloatRepr(f)
		}
		return s
	}
	return FloatRepr(f)
}

// PyStr is Python's str() of a JSON value: None/True/False, strings as-is, ints without a fraction, floats by
// FloatRepr, lists and dicts as their repr. json.Number keeps the int/float distinction of its literal; a plain
// float64 is always a Python float ("3.0"). pj.Obj keys print sorted (Go maps have no order); use *Map to keep one.
func PyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return PyRepr(v)
}

// PyRepr is Python's repr() of a JSON value (strings quoted the way Python quotes them).
func PyRepr(v any) string {
	var b strings.Builder
	writeRepr(&b, v)
	return b.String()
}

func writeRepr(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("None")
	case bool:
		if x {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case string:
		b.WriteString(strRepr(x))
	case json.Number:
		b.WriteString(numberStr(x))
	case float64:
		b.WriteString(FloatRepr(x))
	case float32:
		b.WriteString(FloatRepr(float64(x)))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeRepr(b, e)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strRepr(e))
		}
		b.WriteByte(']')
	case pj.Obj:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strRepr(k))
			b.WriteString(": ")
			writeRepr(b, x[k])
		}
		b.WriteByte('}')
	case *Map:
		b.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strRepr(k))
			b.WriteString(": ")
			writeRepr(b, x.Get(k))
		}
		b.WriteByte('}')
	default:
		fmt.Fprint(b, x)
	}
}

// strRepr is Python's repr() of a str: single quotes unless the string has a ' and no ", backslash escapes for
// \\, the quote, \t \n \r, and \xNN / \uNNNN / \UNNNNNNNN for non-printable characters.
func strRepr(s string) string {
	q := byte('\'')
	if strings.IndexByte(s, '\'') >= 0 && strings.IndexByte(s, '"') < 0 {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == rune(q) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case !pyPrintable(r):
			switch {
			case r <= 0xff:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

// pyPrintable approximates str.isprintable() for one non-ASCII character: not in categories Cc, Cf, Cs, Co, Cn,
// Zl, Zp or Zs.
func pyPrintable(r rune) bool {
	if r == utf8.RuneError {
		return true
	}
	if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zl, unicode.Zp, unicode.Zs) {
		return false
	}
	// Cn (unassigned): not in any category table.
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}

// pyIsSpace is str.isspace() for one character (Unicode White_Space plus the \x1c-\x1f separators Python counts).
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip() with no arguments.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyLower is str.lower(): Unicode full lowercase mapping, including the two context/special cases Go's simple
// mapping misses (İ -> i̇, and final sigma).
func pyLower(s string) string {
	if !strings.ContainsRune(s, 'İ') && !strings.ContainsRune(s, 'Σ') {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case 'İ':
			b.WriteString("i̇")
		case 'Σ':
			if finalSigma(rs, i) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// finalSigma is Unicode's Final_Sigma condition: a cased letter before (skipping case-ignorables) and none after.
func finalSigma(rs []rune, i int) bool {
	cased := func(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }
	ignorable := func(r rune) bool {
		return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) || r == '\'' || r == '.' || r == ':' || r == '·'
	}
	before := false
	for j := i - 1; j >= 0; j-- {
		if ignorable(rs[j]) {
			continue
		}
		before = cased(rs[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if ignorable(rs[j]) {
			continue
		}
		return !cased(rs[j])
	}
	return true
}

// PyDumps is Python's json.dumps(v) with default arguments, byte for byte: ", " and ": " separators, non-ASCII
// escaped as \uXXXX (surrogate pairs above the BMP), floats by FloatRepr (NaN/Infinity as Python writes them).
// *Map keeps its key order; pj.Obj keys are written sorted. Other Go values go through encoding/json first.
func PyDumps(v any) string {
	var b strings.Builder
	dumpValue(&b, v)
	return b.String()
}

func dumpValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		dumpString(b, x)
	case json.Number:
		b.WriteString(numberStr(x))
	case float64:
		dumpFloat(b, x)
	case float32:
		dumpFloat(b, float64(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpValue(b, e)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, e)
		}
		b.WriteByte(']')
	case *Map:
		b.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, k)
			b.WriteString(": ")
			dumpValue(b, x.Get(k))
		}
		b.WriteByte('}')
	case pj.Obj:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, k)
			b.WriteString(": ")
			dumpValue(b, x[k])
		}
		b.WriteByte('}')
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			panic(err)
		}
		parsed, err := loads(string(raw), true)
		if err != nil {
			panic(err)
		}
		dumpValue(b, parsed)
	}
}

func dumpFloat(b *strings.Builder, f float64) {
	switch {
	case math.IsNaN(f):
		b.WriteString("NaN")
	case math.IsInf(f, 1):
		b.WriteString("Infinity")
	case math.IsInf(f, -1):
		b.WriteString("-Infinity")
	default:
		b.WriteString(FloatRepr(f))
	}
}

func dumpString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				b.WriteRune(r)
			case r < 0x10000:
				fmt.Fprintf(b, `\u%04x`, r)
			default:
				r -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800|(r>>10)&0x3ff, 0xdc00|r&0x3ff)
			}
		}
	}
	b.WriteByte('"')
}

// marshalCompact is encoding/json without HTML escaping or a trailing newline.
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
