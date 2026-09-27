package exif

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mononendev/photosort/internal/py"
)

// Python's str()/repr()/float() for the handful of value kinds a tag can hold, so a tag rendered or parsed here
// reads exactly as exifread or Pillow rendered it in the Python this replaces. The generic ones (str.strip,
// repr(float)) are package py's.

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
	s = py.Strip(s)
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
