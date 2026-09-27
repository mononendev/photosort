package py

import (
	"errors"
	"strings"
)

// CSVField is how csv.writer renders a value: None as an empty field, anything else as str().
func CSVField(v any) string {
	if v == nil {
		return ""
	}
	return Str(v)
}

// WriteCSVRow appends one row in Python's default csv dialect ("excel"): comma-separated, a field quoted only when
// it contains a comma, a double quote, \r or \n (quotes inside doubled), rows ended by \r\n. A row that is a single
// empty field is written as "" so it doesn't read back as no row.
func WriteCSVRow(b *strings.Builder, fields []string) {
	if len(fields) == 1 && fields[0] == "" {
		b.WriteString("\"\"\r\n")
		return
	}
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		if strings.ContainsAny(f, ",\"\r\n") {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(f, `"`, `""`))
			b.WriteByte('"')
		} else {
			b.WriteString(f)
		}
	}
	b.WriteString("\r\n")
}

// CSVFieldLimit is csv.field_size_limit()'s default.
const CSVFieldLimit = 131072

// ReadCSV parses text the way csv.reader(io.StringIO(text)) does in Python 3.10 with the default dialect
// (non-strict): lines are split after each \n; a quoted field may span lines; a stray quote inside an unquoted
// field is literal; text after a closing quote is appended; an unterminated quote runs to the end of the input.
// A blank line yields an empty record (DictReader skips those). It fails where Python raises csv.Error: a NUL
// character, a lone \r followed by more text on the same line, or a field over CSVFieldLimit.
func ReadCSV(text string) ([][]string, error) {
	const (
		startRecord = iota
		startField
		inField
		inQuoted
		quoteInQuoted
		eatCRNL
	)
	var (
		records [][]string
		fields  []string
		field   []rune
		state   = startRecord
	)
	save := func() {
		fields = append(fields, string(field))
		field = field[:0]
	}
	add := func(c rune) error {
		if len(field) >= CSVFieldLimit {
			return errors.New("field larger than field limit (131072)")
		}
		field = append(field, c)
		return nil
	}
	const eol = -1
	process := func(c rune) error {
		switch state {
		case startRecord:
			if c == eol {
				return nil // empty line: record []
			}
			if c == '\n' || c == '\r' {
				state = eatCRNL
				return nil
			}
			state = startField
			fallthrough
		case startField:
			switch {
			case c == '\n' || c == '\r' || c == eol:
				save()
				if c == eol {
					state = startRecord
				} else {
					state = eatCRNL
				}
			case c == '"':
				state = inQuoted
			case c == ',':
				save()
			default:
				state = inField
				return add(c)
			}
		case inField:
			switch {
			case c == '\n' || c == '\r' || c == eol:
				save()
				if c == eol {
					state = startRecord
				} else {
					state = eatCRNL
				}
			case c == ',':
				save()
				state = startField
			default:
				return add(c)
			}
		case inQuoted:
			switch {
			case c == eol:
			case c == '"':
				state = quoteInQuoted
			default:
				return add(c)
			}
		case quoteInQuoted:
			switch {
			case c == '"':
				state = inQuoted
				return add(c)
			case c == ',':
				save()
				state = startField
			case c == '\n' || c == '\r' || c == eol:
				save()
				if c == eol {
					state = startRecord
				} else {
					state = eatCRNL
				}
			default:
				state = inField
				return add(c)
			}
		case eatCRNL:
			switch {
			case c == '\n' || c == '\r':
			case c == eol:
				state = startRecord
			default:
				return errors.New("new-line character seen in unquoted field - do you need to open the file in universal-newline mode?")
			}
		}
		return nil
	}

	for len(text) > 0 {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i+1], text[i+1:]
		} else {
			text = ""
		}
		for _, c := range line {
			if c == 0 {
				return nil, errors.New("line contains NUL")
			}
			if err := process(c); err != nil {
				return nil, err
			}
		}
		if err := process(eol); err != nil {
			return nil, err
		}
		if state == startRecord {
			records = append(records, fields)
			fields = nil
		}
	}
	if len(field) != 0 || state == inQuoted { // end of input inside a record
		save()
		records = append(records, fields)
	}
	return records, nil
}
