package schema

import (
	"bytes"

	"github.com/mononendev/photosort/internal/pj"
)

// Map is a JSON object that keeps its keys in insertion order, like a Python dict. The model APIs get the JSON
// schema and request bodies in the same key order the Python code produced, and the UI's request view shows them
// that way; a Go map would sort them. Marshals as a JSON object; PyDumps and PyRepr keep the order too.
type Map struct {
	keys []string
	vals map[string]any
}

// NewMap builds a Map from alternating key, value arguments.
func NewMap(kv ...any) *Map {
	m := &Map{vals: make(map[string]any, len(kv)/2)}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Set(kv[i].(string), kv[i+1])
	}
	return m
}

// Set sets k (a new key goes last; an existing one keeps its place, as in a dict) and returns m.
func (m *Map) Set(k string, v any) *Map {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
	return m
}

// Get returns the value at k, nil when absent.
func (m *Map) Get(k string) any {
	if m == nil {
		return nil
	}
	return m.vals[k]
}

// Has reports whether k is present.
func (m *Map) Has(k string) bool {
	if m == nil {
		return false
	}
	_, ok := m.vals[k]
	return ok
}

// M returns the value at k as a *Map, nil when absent or not a Map.
func (m *Map) M(k string) *Map {
	x, _ := m.Get(k).(*Map)
	return x
}

// Keys returns the keys in order.
func (m *Map) Keys() []string {
	if m == nil {
		return nil
	}
	return m.keys
}

// Len is the number of keys.
func (m *Map) Len() int { return len(m.Keys()) }

// Obj converts m (and nested Maps) to plain pj values, dropping the order.
func (m *Map) Obj() pj.Obj {
	if m == nil {
		return nil
	}
	o := make(pj.Obj, len(m.keys))
	for _, k := range m.keys {
		o[k] = Plain(m.vals[k])
	}
	return o
}

// Plain converts any *Map inside v to pj.Obj, recursively (for code that walks pj values).
func Plain(v any) any {
	switch x := v.(type) {
	case *Map:
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

// MarshalJSON writes the object in key order, without HTML escaping.
func (m *Map) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := marshalCompact(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := marshalCompact(m.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
