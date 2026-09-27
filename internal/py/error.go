package py

import (
	"encoding/json"
	"fmt"

	"github.com/mononendev/photosort/internal/pj"
)

// Error is an error that reads the way the Python code reported it: callers there formatted exceptions as
// f"{type(e).__name__}: {e}", and those strings end up in the database and the UI. Error() returns exactly that.
type Error struct {
	Type string // Python exception class name, e.g. "ValueError", "KeyError", "JSONDecodeError"
	Msg  string // str(e)
}

func (e *Error) Error() string { return e.Type + ": " + e.Msg }

// Errorf builds an *Error of Python exception class typ with a formatted message.
func Errorf(typ, format string, a ...any) *Error {
	return &Error{Type: typ, Msg: fmt.Sprintf(format, a...)}
}

// KeyError is Python's KeyError for a missing dict key: str() of it is the key's repr.
func KeyError(k any) *Error { return &Error{Type: "KeyError", Msg: Repr(k)} }

// TypeName is type(v).__name__ for a JSON value as this package represents them.
func TypeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if IntLiteral(x) {
			return "int"
		}
		return "float"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, BigInt:
		return "int"
	case float32, float64:
		return "float"
	case []any, []string:
		return "list"
	case pj.Obj, *Object:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}
