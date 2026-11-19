// Package convert turns an OpenAPI (Swagger 2.0 or OpenAPI 3) document into
// a directory of JSON Schema files.
//
// It is a port of instrumenta/openapi2jsonschema and aims to produce
// byte-identical output, quirks included. To get there it models a document
// the way Python does: maps keep insertion order, maps and lists are shared
// by pointer so in-place changes show up wherever the object is referenced,
// and errors that Python raises as exceptions are raised here as panics of
// type *PyError and recovered at the same places Python catches them.
package convert

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// A document value is one of:
//
//	*Map, *List      containers, shared by pointer
//	string, bool, nil
//	Int              an integer, kept as canonical decimal text
//	float64          a float
//	*Ref             a lazily resolved "$ref" (the JsonRef proxy in Python)

// Int is an integer of any size, stored as canonical decimal text.
type Int string

// Map is a JSON object that keeps keys in insertion order, like a Python dict.
type Map struct {
	keys  []string
	index map[string]int
	vals  []any
}

func NewMap() *Map { return &Map{index: map[string]int{}} }

func (m *Map) Len() int       { return len(m.keys) }
func (m *Map) Keys() []string { return m.keys }

func (m *Map) Get(k string) (any, bool) {
	i, ok := m.index[k]
	if !ok {
		return nil, false
	}
	return m.vals[i], true
}

func (m *Map) Has(k string) bool {
	_, ok := m.index[k]
	return ok
}

// Set replaces the value of an existing key in place, or appends a new key,
// matching Python dict assignment.
func (m *Map) Set(k string, v any) {
	if i, ok := m.index[k]; ok {
		m.vals[i] = v
		return
	}
	m.index[k] = len(m.keys)
	m.keys = append(m.keys, k)
	m.vals = append(m.vals, v)
}

// SetDefault is Python's dict.setdefault.
func (m *Map) SetDefault(k string, v any) {
	if !m.Has(k) {
		m.Set(k, v)
	}
}

// shallowCopy returns a new map with the same entries.
func (m *Map) shallowCopy() *Map {
	return &Map{keys: slices.Clone(m.keys), index: maps.Clone(m.index), vals: slices.Clone(m.vals)}
}

// Each calls fn for every entry in order.
func (m *Map) Each(fn func(k string, v any)) {
	for i, k := range m.keys {
		fn(k, m.vals[i])
	}
}

// List is a JSON array. It is a pointer type so it can be shared and
// appended to in place, like a Python list.
type List struct {
	Items []any
}

func NewList(items ...any) *List { return &List{Items: items} }

// PyError is a Python exception: Class is the exception type name and Msg is
// what str(exception) returns.
type PyError struct {
	Class string
	Msg   string
}

func (e *PyError) Error() string { return e.Msg }

func raise(class, format string, args ...any) {
	panic(&PyError{Class: class, Msg: fmt.Sprintf(format, args...)})
}

// catch runs fn and returns any *PyError it raises. Other panics propagate.
func catch(fn func()) (err *PyError) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*PyError)
			if !ok {
				panic(r)
			}
			err = e
		}
	}()
	fn()
	return nil
}

// maxDepth stands in for Python's recursion limit. Real schemas are a few
// dozen levels deep, so only reference cycles get near it.
const maxDepth = 1000

func checkDepth(depth int) {
	if depth > maxDepth {
		raise("RecursionError", "maximum recursion depth exceeded")
	}
}

// deref follows a *Ref to the value it points at, resolving it if needed.
// This is what touching a JsonRef proxy does in Python.
func deref(v any) any {
	if r, ok := v.(*Ref); ok {
		return r.Subject()
	}
	return v
}

// pyContains is Python's `item in container`.
func pyContains(container, item any) bool {
	switch c := deref(container).(type) {
	case *Map:
		k, ok := deref(item).(string)
		return ok && c.Has(k)
	case *List:
		for _, x := range c.Items {
			if pyEqual(x, item) {
				return true
			}
		}
		return false
	case string:
		s, ok := deref(item).(string)
		if !ok {
			raise("TypeError", "'in <string>' requires string as left operand, not %s", pyTypeName(item))
		}
		return strings.Contains(c, s)
	default:
		raise("TypeError", "argument of type '%s' is not iterable", pyTypeName(c))
		return false
	}
}

// pyGetItem is Python's container[key] for a string key.
func pyGetItem(container any, key string) any {
	switch c := deref(container).(type) {
	case *Map:
		v, ok := c.Get(key)
		if !ok {
			raise("KeyError", "%s", pyRepr(key))
		}
		return v
	case *List:
		raise("TypeError", "list indices must be integers or slices, not str")
	case string:
		raise("TypeError", "string indices must be integers, not 'str'")
	case nil:
		raise("TypeError", "'NoneType' object is not subscriptable")
	default:
		raise("TypeError", "'%s' object is not subscriptable", pyTypeName(c))
	}
	return nil
}

// pyIter is Python's `for x in v`: list items, or the keys of a map.
func pyIter(v any) []any {
	switch c := deref(v).(type) {
	case *List:
		return c.Items
	case *Map:
		out := make([]any, len(c.keys))
		for i, k := range c.keys {
			out[i] = k
		}
		return out
	case string:
		out := []any{}
		for _, r := range c {
			out = append(out, string(r))
		}
		return out
	default:
		raise("TypeError", "'%s' object is not iterable", pyTypeName(c))
		return nil
	}
}

// pyTruthy is Python's bool(v).
func pyTruthy(v any) bool {
	switch c := deref(v).(type) {
	case nil:
		return false
	case bool:
		return c
	case string:
		return c != ""
	case Int:
		return c != "0"
	case float64:
		return c != 0
	case *Map:
		return c.Len() > 0
	case *List:
		return len(c.Items) > 0
	}
	return true
}

// pyEqual is Python's ==, close enough for JSON values.
func pyEqual(a, b any) bool {
	a, b = deref(a), deref(b)
	if na, ok := pyNumber(a); ok {
		nb, ok := pyNumber(b)
		return ok && na == nb
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case *Map:
		y, ok := b.(*Map)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for i, k := range x.keys {
			yv, ok := y.Get(k)
			if !ok || !pyEqual(x.vals[i], yv) {
				return false
			}
		}
		return true
	case *List:
		y, ok := b.(*List)
		if !ok || len(x.Items) != len(y.Items) {
			return false
		}
		for i := range x.Items {
			if !pyEqual(x.Items[i], y.Items[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// pyNumber gives bools, ints and floats a common form for comparison, as
// Python treats True == 1 == 1.0.
func pyNumber(v any) (string, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return "1", true
		}
		return "0", true
	case Int:
		return string(x), true
	case float64:
		if x == float64(int64(x)) && x < 1e18 && x > -1e18 {
			return fmt.Sprint(int64(x)), true
		}
		return pyFloatRepr(x), true
	}
	return "", false
}

func pyTypeName(v any) string {
	switch deref(v).(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case Int:
		return "int"
	case float64:
		return "float"
	case *Map:
		return "dict"
	case *List:
		return "list"
	}
	return "object"
}

// pyRepr is Python's repr() for a string.
func pyRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	var b strings.Builder
	b.WriteString(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == q:
			b.WriteString(`\` + q)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(q)
	return b.String()
}
