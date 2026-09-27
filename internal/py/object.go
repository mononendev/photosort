package py

import (
	"bytes"
	"encoding/json"

	"github.com/mononendev/photosort/internal/pj"
)

// Object is a JSON object that keeps its keys in insertion order, as a Python dict does. A key set twice keeps its
// first position and its last value (dict semantics, and what json.loads does with duplicate keys). The model APIs
// get request bodies and the JSON schema in the key order the Python code produced, and results.jsonl rows keep
// theirs; a Go map would sort them. The zero value and a nil *Object are empty objects.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject builds an object from alternating key, value arguments.
func NewObject(kv ...any) *Object {
	o := &Object{vals: make(map[string]any, len(kv)/2)}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Set adds or replaces a key, keeping the position of an existing one, and returns o.
func (o *Object) Set(k string, v any) *Object {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
	return o
}

// Get returns the value at k, nil when absent.
func (o *Object) Get(k string) any {
	v, _ := o.Lookup(k)
	return v
}

// Lookup is Get with a presence flag: (value, true) for a present key even when its value is null.
func (o *Object) Lookup(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

// Has reports whether k is present.
func (o *Object) Has(k string) bool {
	_, ok := o.Lookup(k)
	return ok
}

// GetOr is dict.get(k, def): def only when the key is absent (a present null stays nil).
func (o *Object) GetOr(k string, def any) any {
	if v, ok := o.Lookup(k); ok {
		return v
	}
	return def
}

// M returns the value at k as an *Object, nil when absent or not an object.
func (o *Object) M(k string) *Object {
	x, _ := o.Get(k).(*Object)
	return x
}

// Keys returns the keys in insertion order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// Len is the number of keys.
func (o *Object) Len() int { return len(o.Keys()) }

// Obj converts o (and nested objects) to plain pj values, dropping the order.
func (o *Object) Obj() pj.Obj {
	if o == nil {
		return nil
	}
	out := make(pj.Obj, len(o.keys))
	for _, k := range o.keys {
		out[k] = Plain(o.vals[k])
	}
	return out
}

// Plain converts any *Object inside v to pj.Obj, recursively (for code that walks pj values).
func Plain(v any) any {
	switch x := v.(type) {
	case *Object:
		return x.Obj()
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Plain(e)
		}
		return out
	case pj.Obj:
		out := make(pj.Obj, len(x))
		for k, e := range x {
			out[k] = Plain(e)
		}
		return out
	}
	return v
}

// MarshalJSON writes compact JSON with the keys in order and no HTML escaping (for HTTP responses; Dumps is the
// Python-format writer).
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
		return o.Lookup(k)
	case map[string]any:
		x, ok := o[k]
		return x, ok
	case Objecter:
		return o.PyObject().Lookup(k)
	}
	return nil, false
}
