// Package py reproduces the Python standard-library behaviours whose exact output photosort's files, requests and
// error strings depend on: json.loads/json.dumps with insertion-ordered objects and Python's int/float distinction,
// str()/repr() of JSON values, exception text, pathlib's name/stem/suffix, str.strip/str.lower, and the csv module's
// excel dialect. Everything here exists so a Go port writes the same bytes the Python did (results.jsonl,
// results.csv, stored JSON columns, model request bodies), not as a general-purpose Python emulation.
package py

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Loads is json.loads for data photosort stored itself (JSON columns, results files): objects become *Object
// (insertion-ordered), integer literals int64 (BigInt beyond int64), other numbers float64, arrays []any.
//
// All three loaders are one port of CPython 3.10's json scanner: they accept what Python accepts (NaN, Infinity and
// -Infinity included, decoded to float64) and fail with an *Error reading exactly like Python's JSONDecodeError
// ("JSONDecodeError: Expecting ',' delimiter: line 1 column 8 (char 7)"). One difference: a lone UTF-16 surrogate
// escape (which Python keeps in its str) becomes U+FFFD, since a Go string can't hold it.
func Loads(s string) (any, error) { return loads(s, true, pyNumber) }

// LoadsNumber is json.loads for model answers and other text whose numbers must stay literal: objects decode to
// pj.Obj (a repeated key keeps the last value, as in Python) and numbers to json.Number holding the literal, so an
// int literal stays distinguishable from a float one (Python's str() of 3 is "3", of 3.0 is "3.0").
func LoadsNumber(s string) (any, error) { return loads(s, false, jsonNumber) }

// LoadsNumberOrdered is LoadsNumber with objects decoded to *Object, keeping key order (for text that is written
// back out or repr()'d).
func LoadsNumberOrdered(s string) (any, error) { return loads(s, true, jsonNumber) }

func jsonNumber(lit string) any { return json.Number(lit) }

// pyNumber is the Go value of a number literal as Python's json module types it: int64 (BigInt beyond) for an int
// literal, float64 otherwise (overflowing to ±inf and underflowing to 0, as float() does).
func pyNumber(lit string) any {
	if IntLiteral(json.Number(lit)) {
		if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return n
		}
		return BigInt(lit)
	}
	f, _ := strconv.ParseFloat(lit, 64) // a scanned literal is always valid; out of range gives ±Inf or 0
	return f
}

type scanner struct {
	s       []rune
	ordered bool             // objects as *Object, else pj.Obj
	number  func(string) any // the value of a number literal
}

// decodeError positions are in characters (code points), like Python's.
func (sc *scanner) decodeError(msg string, pos int) *Error {
	line := 1
	lastNL := -1
	for i := 0; i < pos && i < len(sc.s); i++ {
		if sc.s[i] == '\n' {
			line++
			lastNL = i
		}
	}
	col := pos - lastNL
	return Errorf("JSONDecodeError", "%s: line %d column %d (char %d)", msg, line, col, pos)
}

// stopIteration is the scanner's "no value starts here" signal; it turns into "Expecting value" at pos.
type stopIteration struct{ pos int }

func (e *stopIteration) Error() string { return "StopIteration" }

func loads(str string, ordered bool, number func(string) any) (any, error) {
	sc := &scanner{s: []rune(str), ordered: ordered, number: number}
	if len(sc.s) > 0 && sc.s[0] == 0xfeff {
		return nil, sc.decodeError("Unexpected UTF-8 BOM (decode using utf-8-sig)", 0)
	}
	idx := sc.skipWS(0)
	v, end, err := sc.scanOnce(idx)
	if err != nil {
		if si, ok := err.(*stopIteration); ok {
			return nil, sc.decodeError("Expecting value", si.pos)
		}
		return nil, err
	}
	end = sc.skipWS(end)
	if end != len(sc.s) {
		return nil, sc.decodeError("Extra data", end)
	}
	return v, nil
}

func (sc *scanner) skipWS(i int) int {
	for i < len(sc.s) {
		switch sc.s[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// hasPrefix reports whether the ASCII word p starts at i.
func (sc *scanner) hasPrefix(i int, p string) bool {
	if i+len(p) > len(sc.s) {
		return false
	}
	for j := 0; j < len(p); j++ {
		if sc.s[i+j] != rune(p[j]) {
			return false
		}
	}
	return true
}

func (sc *scanner) scanOnce(idx int) (any, int, error) {
	if idx < 0 || idx >= len(sc.s) {
		return nil, 0, &stopIteration{idx}
	}
	switch sc.s[idx] {
	case '"':
		return sc.scanString(idx + 1)
	case '{':
		return sc.parseObject(idx + 1)
	case '[':
		return sc.parseArray(idx + 1)
	case 'n':
		if sc.hasPrefix(idx, "null") {
			return nil, idx + 4, nil
		}
	case 't':
		if sc.hasPrefix(idx, "true") {
			return true, idx + 4, nil
		}
	case 'f':
		if sc.hasPrefix(idx, "false") {
			return false, idx + 5, nil
		}
	case 'N':
		if sc.hasPrefix(idx, "NaN") {
			return math.NaN(), idx + 3, nil
		}
	case 'I':
		if sc.hasPrefix(idx, "Infinity") {
			return math.Inf(1), idx + 8, nil
		}
	case '-':
		if sc.hasPrefix(idx, "-Infinity") {
			return math.Inf(-1), idx + 9, nil
		}
	}
	return sc.matchNumber(idx)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func (sc *scanner) matchNumber(start int) (any, int, error) {
	s, idx, last := sc.s, start, len(sc.s)-1
	if s[idx] == '-' {
		idx++
		if idx > last {
			return nil, 0, &stopIteration{start}
		}
	}
	switch {
	case s[idx] >= '1' && s[idx] <= '9':
		idx++
		for idx <= last && isDigit(s[idx]) {
			idx++
		}
	case s[idx] == '0':
		idx++
	default:
		return nil, 0, &stopIteration{start}
	}
	if idx < last && s[idx] == '.' && isDigit(s[idx+1]) {
		idx += 2
		for idx <= last && isDigit(s[idx]) {
			idx++
		}
	}
	if idx < last && (s[idx] == 'e' || s[idx] == 'E') {
		eStart := idx
		idx++
		if idx < last && (s[idx] == '-' || s[idx] == '+') {
			idx++
		}
		for idx <= last && isDigit(s[idx]) {
			idx++
		}
		if !isDigit(s[idx-1]) {
			idx = eStart
		}
	}
	return sc.number(string(s[start:idx])), idx, nil
}

// scanString follows CPython's scanstring_unicode, including its error positions. end is the index just after the
// opening quote.
func (sc *scanner) scanString(end int) (any, int, error) {
	s, n := sc.s, len(sc.s)
	begin := end - 1
	var b strings.Builder
	for {
		var c rune
		next := end
		for ; next < n; next++ {
			c = s[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return nil, 0, sc.decodeError("Invalid control character at", next)
			}
		}
		if next >= n {
			c = 0
		}
		b.WriteString(string(s[end:next]))
		if c == '"' {
			return b.String(), next + 1, nil
		}
		if c != '\\' {
			return nil, 0, sc.decodeError("Unterminated string starting at", begin)
		}
		next++ // skip the backslash
		if next == n {
			return nil, 0, sc.decodeError("Unterminated string starting at", begin)
		}
		c = s[next]
		if c != 'u' {
			end = next + 1
			switch c {
			case '"', '\\', '/':
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			default:
				return nil, 0, sc.decodeError("Invalid \\escape", end-2)
			}
			b.WriteRune(c)
			continue
		}
		next++
		end = next + 4
		if end >= n {
			return nil, 0, sc.decodeError("Invalid \\uXXXX escape", next-1)
		}
		cp, ok := hex4(s[next:end])
		if !ok {
			return nil, 0, sc.decodeError("Invalid \\uXXXX escape", end-5)
		}
		next = end
		if utf16.IsSurrogate(cp) && cp < 0xdc00 && end+6 < n && s[next] == '\\' && s[next+1] == 'u' {
			end += 6
			c2, ok := hex4(s[next+2 : end])
			if !ok {
				return nil, 0, sc.decodeError("Invalid \\uXXXX escape", end-5)
			}
			if c2 >= 0xdc00 && c2 <= 0xdfff {
				cp = utf16.DecodeRune(cp, c2)
			} else {
				end -= 6
			}
		}
		b.WriteRune(cp) // a lone surrogate becomes U+FFFD (Python would keep it; Go strings can't)
	}
}

func hex4(rs []rune) (rune, bool) {
	var c rune
	for _, d := range rs {
		c <<= 4
		switch {
		case d >= '0' && d <= '9':
			c |= d - '0'
		case d >= 'a' && d <= 'f':
			c |= d - 'a' + 10
		case d >= 'A' && d <= 'F':
			c |= d - 'A' + 10
		default:
			return 0, false
		}
	}
	return c, true
}

func (sc *scanner) parseObject(idx int) (any, int, error) {
	s, last := sc.s, len(sc.s)-1
	var obj map[string]any
	var om *Object
	if sc.ordered {
		om = NewObject()
	} else {
		obj = map[string]any{}
	}
	idx = sc.skipWS(idx)
	if idx > last || s[idx] != '}' {
		for {
			if idx > last || s[idx] != '"' {
				return nil, 0, sc.decodeError("Expecting property name enclosed in double quotes", idx)
			}
			k, next, err := sc.scanString(idx + 1)
			if err != nil {
				return nil, 0, err
			}
			idx = sc.skipWS(next)
			if idx > last || s[idx] != ':' {
				return nil, 0, sc.decodeError("Expecting ':' delimiter", idx)
			}
			idx = sc.skipWS(idx + 1)
			v, next, err := sc.scanOnce(idx)
			if err != nil {
				return nil, 0, err
			}
			if om != nil {
				om.Set(k.(string), v)
			} else {
				obj[k.(string)] = v
			}
			idx = sc.skipWS(next)
			if idx <= last && s[idx] == '}' {
				break
			}
			if idx > last || s[idx] != ',' {
				return nil, 0, sc.decodeError("Expecting ',' delimiter", idx)
			}
			idx = sc.skipWS(idx + 1)
		}
	}
	if om != nil {
		return om, idx + 1, nil
	}
	return obj, idx + 1, nil
}

func (sc *scanner) parseArray(idx int) (any, int, error) {
	s, last := sc.s, len(sc.s)-1
	arr := []any{}
	idx = sc.skipWS(idx)
	if idx > last || s[idx] != ']' {
		for {
			v, next, err := sc.scanOnce(idx)
			if err != nil {
				return nil, 0, err
			}
			arr = append(arr, v)
			idx = sc.skipWS(next)
			if idx <= last && s[idx] == ']' {
				break
			}
			if idx > last || s[idx] != ',' {
				return nil, 0, sc.decodeError("Expecting ',' delimiter", idx)
			}
			idx = sc.skipWS(idx + 1)
		}
	}
	return arr, idx + 1, nil
}

// Dumps is json.dumps(v) with Python's default arguments, byte for byte: ", " and ": " separators, ensure_ascii
// (every character outside printable ASCII escaped as \uXXXX, surrogate pairs above the BMP), floats as repr() with
// NaN/Infinity, and objects in insertion order. A json.Number keeps its literal's int/float kind; a plain map
// (pj.Obj) has no order and is written with sorted keys. Other Go values go through encoding/json first.
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
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case BigInt:
		b.WriteString(string(x))
	case json.Number:
		b.WriteString(numberStr(x))
	case float32:
		dumpFloat(b, float64(x))
	case float64:
		dumpFloat(b, x)
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
		dump(b, sortedObject(x))
	default:
		raw, err := marshal(x)
		if err != nil {
			panic(fmt.Sprintf("py.Dumps: %T: %v", v, err))
		}
		parsed, err := LoadsNumberOrdered(string(raw))
		if err != nil {
			panic(fmt.Sprintf("py.Dumps: %T: %v", v, err))
		}
		dump(b, parsed)
	}
}

// sortedObject is m as an *Object with its keys sorted (how an unordered map is written).
func sortedObject(m map[string]any) *Object {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	o := &Object{}
	for _, k := range keys {
		o.Set(k, m[k])
	}
	return o
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

// FloatRepr is Python's repr()/str() of a float: the shortest string that round-trips, in fixed notation when the
// decimal exponent is in [-4, 16) (with ".0" on integral values), else d.ddde±XX. inf/nan print as "inf"/"nan".
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // e.g. -1.2345e+06 (zero is 0e+00, giving "0.0")
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

// IntLiteral reports whether n is an int literal, i.e. whether Python's json would have decoded it to an int.
func IntLiteral(n json.Number) bool { return !strings.ContainsAny(string(n), ".eEnN") }

// numberStr is str() of a JSON number literal as Python's json module would have parsed it: an int literal prints
// as the (normalized) integer, anything with a fraction or exponent as a float.
func numberStr(n json.Number) string {
	s := string(n)
	if IntLiteral(n) {
		if b, ok := new(big.Int).SetString(s, 10); ok {
			return b.String()
		}
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !isRangeErr(err) { // out of range is fine: Python's float() gives inf/0, as ParseFloat does
		return s
	}
	return FloatRepr(f)
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

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}
