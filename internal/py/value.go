package py

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Str is Python's str() of a JSON value: None/True/False, ints in decimal, floats as repr, strings as is, and
// lists/dicts as their repr.
func Str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return Repr(v)
}

// Repr is Python's repr() of a JSON value.
func Repr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return reprStr(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case BigInt:
		return string(x)
	case float64:
		return FloatRepr(x)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = reprStr(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = Repr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Object:
		parts := make([]string, 0, x.Len())
		for _, k := range x.Keys() {
			parts = append(parts, reprStr(k)+": "+Repr(x.vals[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case Objecter:
		return Repr(x.PyObject())
	}
	if f, ok := toFloat(v); ok {
		return FloatRepr(f)
	}
	return "<?>"
}

// reprStr is repr(str): single quotes unless the text has a ' and no ", escapes for \\ and the quote, \t \n \r,
// \xNN / \uNNNN / \UNNNNNNNN for non-printable characters.
func reprStr(s string) string {
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
			b.WriteString(`\x` + hex2(r))
		case r < 0x7f || unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			b.WriteString(`\x` + hex2(r))
		case r < 0x10000:
			b.WriteString(`\u` + pad(strconv.FormatInt(int64(r), 16), 4))
		default:
			b.WriteString(`\U` + pad(strconv.FormatInt(int64(r), 16), 8))
		}
	}
	b.WriteByte(q)
	return b.String()
}

func hex2(r rune) string { return pad(strconv.FormatInt(int64(r), 16), 2) }

func pad(s string, n int) string { return strings.Repeat("0", n-len(s)) + s }

// Truthy is Python's bool() of a JSON value.
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
	case []string:
		return len(x) > 0
	case *Object:
		return x.Len() > 0
	case map[string]any:
		return len(x) > 0
	case BigInt:
		return true // a BigInt is never zero
	}
	if f, ok := toFloat(v); ok {
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

// Number reads a Python number (bools count, as True == 1): (value, true), or false for anything else.
func Number(v any) (float64, bool) {
	if b, ok := v.(bool); ok {
		if b {
			return 1, true
		}
		return 0, true
	}
	return toFloat(v)
}

// Eq is Python's == on JSON values: numbers (and bools) by value, strings by text, lists and objects element-wise.
func Eq(a, b any) bool {
	if fa, ok := Number(a); ok {
		fb, ok := Number(b)
		return ok && fa == fb
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.Keys() {
			yv, ok := y.Get(k)
			if !ok || !Eq(x.vals[k], yv) {
				return false
			}
		}
		return true
	}
	la, oka := List(a)
	lb, okb := List(b)
	if oka && okb {
		if len(la) != len(lb) {
			return false
		}
		for i := range la {
			if !Eq(la[i], lb[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// Less is Python's < on numbers; (false, false) when either side isn't a number.
func Less(a, b any) (less, ok bool) {
	fa, ok1 := Number(a)
	fb, ok2 := Number(b)
	return fa < fb, ok1 && ok2
}

// IntKey is how a small-int-keyed Python dict looks a value up: an int, an integral float or a bool (True == 1
// hashes like 1) finds key n. Anything else finds nothing.
func IntKey(v any) (int, bool) {
	f, ok := Number(v)
	if !ok || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return int(f), true
}

// List is list(v) for the iterable values a record can carry: a JSON array, a []string, a string (a list of its
// characters, as list("ab") is) or an object (its keys). false for anything else (list(None) raises).
func List(v any) ([]any, bool) {
	switch x := v.(type) {
	case *Object:
		out := make([]any, 0, x.Len())
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out, true
	case []any:
		return x, true
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, true
	case string:
		out := make([]any, 0, utf8.RuneCountInString(x))
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, true
	}
	return nil, false
}

// IsSpace is str.isspace() for one character (also what re's \s matches in a str pattern).
func IsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, ' ', 0x85, 0xa0, 0x1680,
		0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// SpaceClass is a Go regexp character class matching exactly Python's \s (Go's own \s lacks \v and Unicode spaces).
const SpaceClass = `[\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`

// Strip is str.strip() with no argument.
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// Lower is str.lower(): per-character lowercase, plus the two context rules Python applies (U+0130 lowers to "i̇",
// and a capital sigma ending a word lowers to final ς).
func Lower(s string) string {
	if !strings.ContainsAny(s, "\u0130\u03a3") {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case 0x130:
			b.WriteString("i\u0307")
		case 0x3a3:
			if finalSigma(rs, i) {
				b.WriteRune(0x3c2)
			} else {
				b.WriteRune(0x3c3)
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func cased(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }

func caseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) ||
		strings.ContainsRune("'.:^`·‘’․‧", r)
}

// finalSigma: preceded by a cased letter and not followed by one (case-ignorable characters skipped both ways).
func finalSigma(rs []rune, i int) bool {
	j := i - 1
	for j >= 0 && caseIgnorable(rs[j]) {
		j--
	}
	if j < 0 || !cased(rs[j]) {
		return false
	}
	j = i + 1
	for j < len(rs) && caseIgnorable(rs[j]) {
		j++
	}
	return j >= len(rs) || !cased(rs[j])
}

// DigitValue is the value of a Unicode decimal digit (category Nd), as int() reads it; -1 for anything else. Nd
// characters come in contiguous runs 0-9, so the value is the character's position in its run.
func DigitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	if !unicode.Is(unicode.Nd, r) {
		return -1
	}
	n := 0
	for unicode.Is(unicode.Nd, r-rune(n)-1) {
		n++
	}
	return n % 10
}

// otherDigits are the non-decimal characters str.isdigit() also accepts (Numeric_Type=Digit: superscripts,
// subscripts, circled digits, ...). int() rejects them.
var otherDigits = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x00b2, Hi: 0x00b3, Stride: 1}, {Lo: 0x00b9, Hi: 0x00b9, Stride: 1},
		{Lo: 0x1369, Hi: 0x1371, Stride: 1}, {Lo: 0x19da, Hi: 0x19da, Stride: 1},
		{Lo: 0x2070, Hi: 0x2070, Stride: 1}, {Lo: 0x2074, Hi: 0x2079, Stride: 1},
		{Lo: 0x2080, Hi: 0x2089, Stride: 1}, {Lo: 0x2460, Hi: 0x2468, Stride: 1},
		{Lo: 0x2474, Hi: 0x247c, Stride: 1}, {Lo: 0x2488, Hi: 0x2490, Stride: 1},
		{Lo: 0x24ea, Hi: 0x24ea, Stride: 1}, {Lo: 0x24f5, Hi: 0x24fd, Stride: 1},
		{Lo: 0x24ff, Hi: 0x24ff, Stride: 1}, {Lo: 0x2776, Hi: 0x277e, Stride: 1},
		{Lo: 0x2780, Hi: 0x2788, Stride: 1}, {Lo: 0x278a, Hi: 0x2792, Stride: 1},
	},
	R32: []unicode.Range32{
		{Lo: 0x10a40, Hi: 0x10a43, Stride: 1}, {Lo: 0x10e60, Hi: 0x10e68, Stride: 1},
		{Lo: 0x11052, Hi: 0x1105a, Stride: 1}, {Lo: 0x1f100, Hi: 0x1f10a, Stride: 1},
	},
}

// IsDigit is str.isdigit(): non-empty and every character a digit (decimal or otherwise).
func IsDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if DigitValue(r) < 0 && !unicode.Is(otherDigits, r) {
			return false
		}
	}
	return true
}

// Int is int(s) for a string: optional surrounding whitespace, an optional sign, decimal digits in any script with
// single underscores between them. false where int() raises ValueError, or beyond int64.
func Int(s string) (int64, bool) {
	s = Strip(s)
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	var n int64
	prevUnderscore := true // no leading underscore
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		d := DigitValue(r)
		if d < 0 {
			return 0, false
		}
		if n > (math.MaxInt64-int64(d))/10 {
			return 0, false
		}
		n = n*10 + int64(d)
		prevUnderscore = false
	}
	if prevUnderscore {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// ValidUTF8 drops invalid UTF-8 bytes, as bytes.decode("utf-8", "ignore") does.
func ValidUTF8(s string) string { return validUTF8(s) }

// UniversalNewlines is what reading a file in text mode does to line endings: \r\n and lone \r become \n.
func UniversalNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}
