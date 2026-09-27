package exif

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Python's str()/repr()/float() for the handful of value kinds a tag can hold, so a tag rendered or parsed here
// reads exactly as exifread or Pillow rendered it in the Python this replaces.

// pyIsSpace is str.isspace() for one rune: Unicode whitespace plus the \x1c-\x1f separators Python counts.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip().
func pyStrip(s string) string {
	return strings.TrimFunc(s, pyIsSpace)
}

// pyG is f"{x:g}": 6 significant digits, trailing zeros dropped, exponent outside [-4, 6).
func pyG(x float64) string {
	switch {
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x):
		return "nan"
	}
	return strconv.FormatFloat(x, 'g', 6, 64)
}

// pyFloatRepr is repr(float): the shortest round-tripping digits, fixed notation for exponents in [-4, 16).
func pyFloatRepr(x float64) string {
	switch {
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x):
		return "nan"
	}
	e := strconv.FormatFloat(x, 'e', -1, 64) // -d.ddde±XX
	sign := ""
	if e[0] == '-' {
		sign, e = "-", e[1:]
	}
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := fmt.Sprintf("%+03d", exp)
		return sign + m + "e" + es
	}
	if exp < 0 {
		return sign + "0." + strings.Repeat("0", -exp-1) + digits
	}
	if len(digits) <= exp+1 {
		return sign + digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	}
	return sign + digits[:exp+1] + "." + digits[exp+1:]
}

// pyBytesRepr is repr(bytes): b'...' with Python's quoting and escapes.
func pyBytesRepr(b []byte) string {
	q := byte('\'')
	if strings.IndexByte(string(b), '\'') >= 0 && strings.IndexByte(string(b), '"') < 0 {
		q = '"'
	}
	var sb strings.Builder
	sb.WriteString("b")
	sb.WriteByte(q)
	for _, c := range b {
		switch {
		case c == q || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c == '\t':
			sb.WriteString(`\t`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte(q)
	return sb.String()
}

// pyFloat is float(s) for a str: surrounding whitespace, a sign, digits with single underscores between them,
// inf/infinity/nan. Hex and other Go-only spellings are rejected.
func pyFloat(s string) (float64, bool) {
	s = pyStrip(s)
	if s == "" {
		return 0, false
	}
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 {
		return 0, false
	}
	switch strings.ToLower(body) {
	case "inf", "infinity":
		if s[0] == '-' {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case "nan":
		return math.NaN(), true
	}
	for i, r := range body {
		switch {
		case r >= '0' && r <= '9', r == '.', r == 'e', r == 'E':
		case (r == '+' || r == '-') && i > 0 && (body[i-1] == 'e' || body[i-1] == 'E'):
		case r == '_':
			if i == 0 || i == len(body)-1 || !isDigit(body[i-1]) || !isDigit(body[i+1]) {
				return 0, false
			}
		default:
			return 0, false
		}
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	if err != nil {
		// ParseFloat reports out-of-range values as errors with ±Inf; Python returns inf too.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
