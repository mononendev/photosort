package db

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Dialect writes the few pieces of SQL that differ between SQLite and Postgres: reading and writing fields inside
// the JSON columns (TEXT in SQLite, JSONB in Postgres), placeholders, and a handful of functions. Everything else is
// plain SQL both accept.
//
// JSON reads come typed, so a comparison means the same thing on both: JNum is a number or NULL, JText a string or
// NULL, JBool a boolean or NULL. On SQLite, json_extract already returns SQL values of the JSON's type (booleans as
// 1/0); on Postgres the helpers only yield a value when the field has the expected JSON type, so a stray string in a
// numeric field reads as NULL instead of failing the whole query.
type Dialect struct{ PG bool }

func (d Dialect) path(keys []string) string {
	if d.PG {
		return "'{" + strings.Join(keys, ",") + "}'"
	}
	return "'$." + strings.Join(keys, ".") + "'"
}

// J is the raw JSON value at col.keys, for IS NULL tests and comparing two JSON values of the same type.
func (d Dialect) J(col string, keys ...string) string {
	if d.PG {
		return fmt.Sprintf("(%s #> %s)", col, d.path(keys))
	}
	return fmt.Sprintf("json_extract(%s, %s)", col, d.path(keys))
}

// JNum is the number at col.keys, or NULL.
func (d Dialect) JNum(col string, keys ...string) string {
	if d.PG {
		p := d.path(keys)
		return fmt.Sprintf("(CASE WHEN jsonb_typeof(%s #> %s) = 'number' THEN (%s #>> %s)::double precision END)", col, p, col, p)
	}
	return d.J(col, keys...)
}

// JText is the string at col.keys, or NULL.
func (d Dialect) JText(col string, keys ...string) string {
	if d.PG {
		p := d.path(keys)
		return fmt.Sprintf("(CASE WHEN jsonb_typeof(%s #> %s) = 'string' THEN %s #>> %s END)", col, p, col, p)
	}
	return d.J(col, keys...)
}

// JBool is the boolean at col.keys, or NULL (1/0 on SQLite; compare with TRUE/FALSE, which both accept).
func (d Dialect) JBool(col string, keys ...string) string {
	if d.PG {
		p := d.path(keys)
		return fmt.Sprintf("(CASE WHEN jsonb_typeof(%s #> %s) = 'boolean' THEN (%s #>> %s)::boolean END)", col, p, col, p)
	}
	return d.J(col, keys...)
}

// JLen is the length of the array at col.keys, or NULL.
func (d Dialect) JLen(col string, keys ...string) string {
	if d.PG {
		p := d.path(keys)
		return fmt.Sprintf("(CASE WHEN jsonb_typeof(%s #> %s) = 'array' THEN jsonb_array_length(%s #> %s) END)", col, p, col, p)
	}
	return fmt.Sprintf("json_array_length(%s, %s)", col, d.path(keys))
}

// JArrayText is a condition: the array at col.keys holds the string bound to the next placeholder.
func (d Dialect) JArrayHas(col string, keys ...string) string {
	if d.PG {
		return fmt.Sprintf("(%s #> %s) @> jsonb_build_array(CAST(? AS text))", col, d.path(keys))
	}
	return fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s, %s) WHERE value = ?)", col, d.path(keys))
}

// SetJ is the JSON document bound to the next placeholder with `key` set to expr (a numeric SQL expression).
func (d Dialect) SetJ(key, expr string) string {
	if d.PG {
		return fmt.Sprintf("jsonb_set(CAST(? AS jsonb), '{%s}', to_jsonb(%s))", key, expr)
	}
	return fmt.Sprintf("json_set(?, '$.%s', %s)", key, expr)
}

// SetJPath is col with the value at keys replaced by expr, a JSON-valued expression built with the same dialect.
func (d Dialect) SetJPath(col string, keys []string, expr string) string {
	if d.PG {
		return fmt.Sprintf("jsonb_set(%s, %s, to_jsonb(%s))", col, d.path(keys), expr)
	}
	return fmt.Sprintf("json_set(%s, %s, %s)", col, d.path(keys), expr)
}

// Least is the smaller of two values (NULL if either is).
func (d Dialect) Least(a, b string) string {
	if d.PG {
		return fmt.Sprintf("(CASE WHEN (%s) IS NULL OR (%s) IS NULL THEN NULL ELSE LEAST(%s, %s) END)", a, b, a, b)
	}
	return fmt.Sprintf("MIN(%s, %s)", a, b)
}

// Round rounds a float expression to n decimals.
func (d Dialect) Round(expr string, n int) string {
	if d.PG {
		return fmt.Sprintf("round((%s)::numeric, %d)::double precision", expr, n)
	}
	return fmt.Sprintf("round(%s, %d)", expr, n)
}

// Rebind turns ? placeholders into $1, $2, ... for Postgres. The SQL here never has ? inside string literals.
func (d Dialect) Rebind(q string) string {
	if !d.PG || !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// EmptyObject is a condition: col holds an empty object ('' too, on SQLite, as the Python version could write).
func (d Dialect) EmptyObject(col string) string {
	if d.PG {
		return fmt.Sprintf("%s = '{}'::jsonb", col)
	}
	return fmt.Sprintf("%s IN ('', '{}')", col)
}

// AsText is a JSON column as text, for LIKE.
func (d Dialect) AsText(col string) string {
	if d.PG {
		return col + "::text"
	}
	return col
}

// Like is SQLite's LIKE: case-insensitive for ASCII.
func (d Dialect) Like() string {
	if d.PG {
		return "ILIKE"
	}
	return "LIKE"
}

// Collate sorts and compares text byte-wise on both, as SQLite does by default (Postgres would use the locale).
func (d Dialect) Collate(expr string) string {
	if d.PG {
		return expr + ` COLLATE "C"`
	}
	return expr
}

// ---- the shared expressions (sort.final_record and friends are their Go twins) ------------------------------------

// EV is the exposure lift the local stage applied, 0 for none.
func (d Dialect) EV() string {
	return fmt.Sprintf("COALESCE(%s, 0)", d.JNum("local_json", "exposure", "ev"))
}

// VLMStale: the model's verdict was made on a different frame than the local stage now has: the exposure lift it
// saw (stamped into vlm_json as seen_ev when stored) no longer matches the local stage's. Results from before the
// lift carry no stamp, which reads as 0: they saw the unlifted frame.
func (d Dialect) VLMStale() string {
	return fmt.Sprintf("(vlm_json IS NOT NULL AND COALESCE(%s, 0) != %s)", d.JNum("vlm_json", "seen_ev"), d.EV())
}

// VLMTodo: images the vision model still has to (re)tag: never tagged, or tagged on a stale frame.
func (d Dialect) VLMTodo() string {
	return "(vlm_json IS NULL OR " + d.VLMStale() + ")"
}

// Review: the two stages disagree (a stale verdict doesn't count: the model judged another frame).
func (d Dialect) Review() string {
	return fmt.Sprintf("local_json IS NOT NULL AND vlm_json IS NOT NULL AND %s != %s AND NOT %s",
		d.JNum("local_json", "local_tier"), d.JNum("vlm_json", "focus_tier"), d.VLMStale())
}

// FocusSources lists the focus sources in a fixed order (vlm first: the default).
var FocusSources = []string{"vlm", "local", "strict"}

// FinalTier is the tier a photo ends up with, as SQL over the images table: your override, else the configured
// focus_source (vlm: the model's, falling back to local until it has run; local: the local stage's; strict: the
// lower of the two). A stale model tier counts as not run yet. Anything unknown reads as vlm, as in
// export.FinalRecord.
func (d Dialect) FinalTier(source string) string {
	ov, loc := d.JNum("override_json", "focus_tier"), d.JNum("local_json", "local_tier")
	vlm := fmt.Sprintf("(CASE WHEN %s THEN NULL ELSE %s END)", d.VLMStale(), d.JNum("vlm_json", "focus_tier"))
	switch source {
	case "local":
		return fmt.Sprintf("COALESCE(%s, %s)", ov, loc)
	case "strict":
		return fmt.Sprintf("COALESCE(%s, CASE WHEN %s IS NULL THEN %s WHEN %s IS NULL THEN %s ELSE %s END)",
			ov, vlm, loc, loc, vlm, d.Least(loc, vlm))
	}
	return fmt.Sprintf("COALESCE(%s, %s, %s)", ov, vlm, loc)
}

// UnderFolder is SQL for col being folder or anything below it, and its arguments. substr() rather than LIKE keeps
// '_' and '%' literal.
func (d Dialect) UnderFolder(folder, col string) (string, []any) {
	folder = strings.TrimRight(folder, "/")
	return fmt.Sprintf("(%s = ? OR substr(%s, 1, CAST(? AS INTEGER)) = ?)", col, col),
		[]any{folder, utf8.RuneCountInString(folder) + 1, folder + "/"}
}
