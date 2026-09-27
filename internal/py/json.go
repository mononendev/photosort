// Package py reproduces the handful of Python standard-library behaviours whose exact output photosort's files and
// responses depend on: json.loads/json.dumps with insertion-ordered objects and Python's int/float distinction,
// str()/repr() of JSON values, pathlib's name/stem/suffix, str.strip/str.lower, and the csv module's excel dialect.
// Everything here exists so a Go port writes the same bytes the Python did (results.jsonl, results.csv, stored JSON
// columns), not as a general-purpose Python emulation.
package py

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Object is a JSON object that keeps its keys in insertion order, as a Python dict does. A key set twice keeps its
// first position and its last value (dict semantics, and what json.loads does with duplicate keys). The zero value
// and a nil *Object are empty objects.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject builds an object from alternating key, value arguments.
func NewObject(kv ...any) *Object {
	o := &Object{}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Set adds or replaces a key, keeping the position of an existing one.
func (o *Object) Set(k string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Get is dict.get with a presence flag: (value, true) for a present key even when its value is null.
func (o *Object) Get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

// GetOr is dict.get(k, def): def only when the key is absent (a present null stays nil).
func (o *Object) GetOr(k string, def any) any {
	if v, ok := o.Get(k); ok {
		return v
	}
	return def
}

// Keys returns the keys in insertion order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// MarshalJSON writes compact JSON with the keys in order (for HTTP responses; Dumps is the Python-format writer).
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.Keys() {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal is json.Marshal without HTML escaping (<, >, & stay as they are, as in Python's output).
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// BigInt is an integer literal too large for int64, kept as its decimal text (Python ints are unbounded).
type BigInt string

// MarshalJSON writes the literal.
func (n BigInt) MarshalJSON() ([]byte, error) { return []byte(n), nil }

// Objecter is implemented by values that serialize as an ordered object (e.g. export.Record).
type Objecter interface {
	PyObject() *Object
}

// Lookup reads a key from an *Object, a map[string]any (pj.Obj) or an Objecter: dict.get with a presence flag.
func Lookup(v any, k string) (any, bool) {
	switch o := v.(type) {
	case *Object:
		return o.Get(k)
	case map[string]any:
		x, ok := o[k]
		return x, ok
	case Objecter:
		return o.PyObject().Get(k)
	}
	return nil, false
}

// Loads is json.loads: objects become *Object (insertion-ordered), integer literals int64 (BigInt beyond int64),
// other numbers float64, arrays []any. Like Python it accepts NaN, Infinity and -Infinity. One difference: a lone
// UTF-16 surrogate escape (which Python keeps in its str) becomes U+FFFD, since a Go string can't hold it.
func Loads(s string) (any, error) {
	p := &parser{s: s}
	p.ws()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, p.errf("Extra data")
	}
	return v, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) errf(msg string) error {
	return fmt.Errorf("json: %s at char %d", msg, p.i)
}

func (p *parser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) lit(word string) bool {
	if strings.HasPrefix(p.s[p.i:], word) {
		p.i += len(word)
		return true
	}
	return false
}

func (p *parser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, p.errf("Expecting value")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case p.lit("null"):
		return nil, nil
	case p.lit("true"):
		return true, nil
	case p.lit("false"):
		return false, nil
	case p.lit("NaN"):
		return math.NaN(), nil
	case p.lit("Infinity"):
		return math.Inf(1), nil
	case p.lit("-Infinity"):
		return math.Inf(-1), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.errf("Expecting value")
}

func (p *parser) object() (any, error) {
	p.i++
	o := &Object{vals: map[string]any{}}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return o, nil
	}
	for {
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.errf("Expecting property name enclosed in double quotes")
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.errf("Expecting ':' delimiter")
		}
		p.i++
		p.ws()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		o.Set(k, v)
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			return o, nil
		}
		return nil, p.errf("Expecting ',' delimiter")
	}
}

func (p *parser) array() (any, error) {
	p.i++
	out := []any{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return out, nil
	}
	for {
		p.ws()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return out, nil
		}
		return nil, p.errf("Expecting ',' delimiter")
	}
}

func (p *parser) str() (string, error) {
	p.i++ // opening quote
	var b strings.Builder
	for {
		if p.i >= len(p.s) {
			return "", p.errf("Unterminated string")
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), nil
		case c < 0x20:
			return "", p.errf("Invalid control character")
		case c != '\\':
			b.WriteByte(c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.s) {
			return "", p.errf("Unterminated string")
		}
		e := p.s[p.i]
		p.i++
		switch e {
		case '"', '\\', '/':
			b.WriteByte(e)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, err := p.hex4()
			if err != nil {
				return "", err
			}
			if utf16.IsSurrogate(r) && strings.HasPrefix(p.s[p.i:], `\u`) {
				save := p.i
				p.i += 2
				r2, err := p.hex4()
				if err == nil && r2 >= 0xDC00 && r2 <= 0xDFFF && r < 0xDC00 {
					r = utf16.DecodeRune(r, r2)
				} else {
					p.i = save
				}
			}
			b.WriteRune(r) // a lone surrogate writes U+FFFD
		default:
			return "", p.errf("Invalid \\escape")
		}
	}
}

func (p *parser) hex4() (rune, error) {
	if p.i+4 > len(p.s) {
		return 0, p.errf("Invalid \\uXXXX escape")
	}
	n, err := strconv.ParseUint(p.s[p.i:p.i+4], 16, 32)
	if err != nil {
		return 0, p.errf("Invalid \\uXXXX escape")
	}
	p.i += 4
	return rune(n), nil
}

func (p *parser) number() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		p.i = start
		return nil, p.errf("Expecting value")
	}
	isFloat := false
	if p.i+1 < len(p.s) && p.s[p.i] == '.' && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
		p.i++
		digits()
		isFloat = true
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		save := p.i
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			p.i = save
		} else {
			isFloat = true
		}
	}
	text := p.s[start:p.i]
	if isFloat {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !isRangeErr(err) { // Python's float() gives inf/0 on overflow/underflow, as ParseFloat does
			return nil, p.errf("bad number")
		}
		return f, nil
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, nil
	}
	return BigInt(text), nil
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// Dumps is json.dumps(v) with Python's defaults: ", " and ": " separators, ensure_ascii (every character outside
// printable ASCII escaped as \uXXXX, surrogate pairs above the BMP), floats as repr() with NaN/Infinity, and objects
// in insertion order. A plain map (pj.Obj) has no order and is written with sorted keys.
func Dumps(v any) string {
	var b strings.Builder
	dump(&b, v)
	return b.String()
}

func dump(b *strings.Builder, v any) {
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
		quote(b, x)
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case BigInt:
		b.WriteString(string(x))
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString("NaN")
		case math.IsInf(x, 1):
			b.WriteString("Infinity")
		case math.IsInf(x, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(FloatRepr(x))
		}
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dump(b, e)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			quote(b, e)
		}
		b.WriteByte(']')
	case *Object:
		b.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				b.WriteString(", ")
			}
			quote(b, k)
			b.WriteString(": ")
			dump(b, x.vals[k])
		}
		b.WriteByte('}')
	case Objecter:
		dump(b, x.PyObject())
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := &Object{}
		for _, k := range keys {
			o.Set(k, x[k])
		}
		dump(b, o)
	default:
		if f, ok := toFloat(v); ok {
			dump(b, f)
			return
		}
		panic(fmt.Sprintf("py.Dumps: unsupported type %T", v))
	}
}

const hexDigits = "0123456789abcdef"

func quote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s { // invalid UTF-8 bytes come through as U+FFFD
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r >= 0x20 && r <= 0x7e:
			b.WriteRune(r)
		case r > 0xFFFF:
			r1, r2 := utf16.EncodeRune(r)
			u4(b, r1)
			u4(b, r2)
		default:
			u4(b, r)
		}
	}
	b.WriteByte('"')
}

func u4(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	for s := 12; s >= 0; s -= 4 {
		b.WriteByte(hexDigits[(r>>s)&0xF])
	}
}

// FloatRepr is Python's repr(float): the shortest round-tripping digits, in fixed notation when the decimal
// exponent is in [-4, 16) (with ".0" on integral values), else d.ddde±XX. inf/nan print as "inf"/"nan".
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // e.g. -1.2345e+06
	sign := ""
	if e[0] == '-' {
		sign, e = "-", e[1:]
	}
	mant, expS, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expS)
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1 // value = 0.digits * 10^decpt
	if decpt > -4 && decpt <= 16 {
		var s string
		switch {
		case decpt <= 0:
			s = "0." + strings.Repeat("0", -decpt) + digits
		case decpt >= len(digits):
			s = digits + strings.Repeat("0", decpt-len(digits)) + ".0"
		default:
			s = digits[:decpt] + "." + digits[decpt:]
		}
		return sign + s
	}
	m := digits[:1]
	if len(digits) > 1 {
		m += "." + digits[1:]
	}
	es := "+"
	if exp < 0 {
		es, exp = "-", -exp
	}
	return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
}

func toFloat(v any) (float64, bool) {
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
	case BigInt:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil || isRangeErr(err)
	}
	return 0, false
}

// validUTF8 drops invalid bytes, as bytes.decode("utf-8", "ignore") does.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "")
}
