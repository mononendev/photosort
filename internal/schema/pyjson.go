package schema

import (
	"encoding/json"
	"math"
	"strings"
	"unicode/utf16"
)

// Loads is Python's json.loads(s) (CPython 3.10, C scanner): it accepts what Python accepts (NaN, Infinity and
// -Infinity included) and fails with the same JSONDecodeError message and position ("Expecting ',' delimiter:
// line 1 column 8 (char 7)"), which is what the backends store as the error of a malformed model answer.
//
// Objects decode to pj.Obj (a repeated key keeps the last value, as in Python). Numbers decode to json.Number
// holding the literal, so an int literal stays distinguishable from a float one (Python's str() of 3 is "3", of 3.0
// is "3.0"); NaN and ±Infinity decode to float64.
func Loads(s string) (any, error) { return loads(s, false) }

// LoadsOrdered is Loads with objects decoded to *Map, keeping key order.
func LoadsOrdered(s string) (any, error) { return loads(s, true) }

type scanner struct {
	s       []rune
	ordered bool
}

// decodeError positions are in characters (code points), like Python's.
func (sc *scanner) decodeError(msg string, pos int) *PyError {
	line := 1
	lastNL := -1
	for i := 0; i < pos && i < len(sc.s); i++ {
		if sc.s[i] == '\n' {
			line++
			lastNL = i
		}
	}
	col := pos - lastNL
	return pyErr("JSONDecodeError", "%s: line %d column %d (char %d)", msg, line, col, pos)
}

// stopIteration is the scanner's "no value starts here" signal; it turns into "Expecting value" at pos.
type stopIteration struct{ pos int }

func (e *stopIteration) Error() string { return "StopIteration" }

func loads(str string, ordered bool) (any, error) {
	sc := &scanner{s: []rune(str), ordered: ordered}
	if len(sc.s) > 0 && sc.s[0] == '\ufeff' {
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

func isWS(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (sc *scanner) skipWS(i int) int {
	for i < len(sc.s) && isWS(sc.s[i]) {
		i++
	}
	return i
}

func (sc *scanner) hasPrefix(i int, p string) bool {
	rp := []rune(p)
	if i+len(rp) > len(sc.s) {
		return false
	}
	for j, r := range rp {
		if sc.s[i+j] != r {
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
	return json.Number(string(s[start:idx])), idx, nil
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
	var om *Map
	if sc.ordered {
		om = NewMap()
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
