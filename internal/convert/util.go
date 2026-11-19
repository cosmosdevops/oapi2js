package convert

// Ports of openapi2jsonschema/util.py. Each walks a map and builds a new
// one, following *Refs as it goes (touching a proxy resolves it in Python
// too). Values that are not maps are returned unchanged, as the Python
// versions do when iteritems() raises AttributeError.

// additionalProperties adds "additionalProperties": false to every nested
// map that has "properties" and no "additionalProperties". It changes those
// maps in place, then returns a copy. It does not look inside lists. This
// recreates kubectl's behaviour.
func additionalProperties(data any) any {
	return additionalPropertiesAt(data, 0)
}

func additionalPropertiesAt(data any, depth int) any {
	checkDepth(depth)
	m, ok := deref(data).(*Map)
	if !ok {
		return data
	}
	out := NewMap()
	for i, k := range m.keys {
		v := m.vals[i]
		newV := v
		if vm, ok := deref(v).(*Map); ok {
			if vm.Has("properties") && !vm.Has("additionalProperties") {
				vm.Set("additionalProperties", false)
			}
			newV = additionalPropertiesAt(v, depth+1)
		}
		out.Set(k, newV)
	}
	return out
}

// replaceIntOrString swaps every map with "format": "int-or-string" for
// {"oneOf": [{"type": "string"}, {"type": "integer"}]}.
func replaceIntOrString(data any) any {
	return replaceIntOrStringAt(data, 0)
}

func replaceIntOrStringAt(data any, depth int) any {
	checkDepth(depth)
	m, ok := deref(data).(*Map)
	if !ok {
		return data
	}
	out := NewMap()
	for i, k := range m.keys {
		v := m.vals[i]
		newV := v
		switch x := deref(v).(type) {
		case *Map:
			if f, ok := x.Get("format"); ok && pyEqual(f, "int-or-string") {
				newV = oneOf("string", "integer")
			} else {
				newV = replaceIntOrStringAt(v, depth+1)
			}
		case *List:
			l := &List{Items: make([]any, len(x.Items))}
			for j, item := range x.Items {
				l.Items[j] = replaceIntOrStringAt(item, depth+1)
			}
			newV = l
		}
		out.Set(k, newV)
	}
	return out
}

// allowNullOptionalFields turns "type": T into "type": [T, "null"] unless
// the field is listed in its grandparent's "required" (the schema that holds
// the "properties" map). "Parent" and "grandparent" count lists as levels
// too, as in the Python.
//
// data is a schema's "properties" and schema is that schema. The Python
// tool passes no schema, so top-level fields can't see their "required"
// list and are always nullable; pass nil to get that behaviour.
func allowNullOptionalFields(data, schema any) any {
	return allowNullAt(data, schema, nil, nil, 0)
}

func allowNullAt(data, parent, grandParent, key any, depth int) any {
	checkDepth(depth)
	m, ok := deref(data).(*Map)
	if !ok {
		return data
	}
	out := NewMap()
	for i, k := range m.keys {
		v := m.vals[i]
		newV := v
		switch x := deref(v).(type) {
		case *Map:
			newV = allowNullAt(v, data, parent, k, depth+1)
		case *List:
			l := &List{Items: make([]any, len(x.Items))}
			for j, item := range x.Items {
				l.Items[j] = allowNullAt(item, v, parent, k, depth+1)
			}
			newV = l
		case string:
			isNonNullType := k == "type" && x != "null"
			isRequired := pyTruthy(grandParent) &&
				pyContains(grandParent, "required") &&
				pyContains(pyGetItem(grandParent, "required"), key)
			if isNonNullType && !isRequired {
				newV = NewList(v, "null")
			}
		}
		out.Set(k, newV)
	}
	return out
}

// changeDictValues copies d, passing every string "$ref" through rewrite.
// The Python version prefixes refs before 3.0 and turns
// "#/components/schemas/X" into "X.json" from 3.0 (see converter.rewriteRef).
func changeDictValues(d any, rewrite func(string) string) any {
	return changeDictValuesAt(d, rewrite, 0)
}

func changeDictValuesAt(d any, rewrite func(string) string, depth int) any {
	checkDepth(depth)
	m, ok := deref(d).(*Map)
	if !ok {
		return d
	}
	out := NewMap()
	for i, k := range m.keys {
		v := m.vals[i]
		newV := v
		switch x := deref(v).(type) {
		case *Map:
			newV = changeDictValuesAt(v, rewrite, depth+1)
		case *List:
			l := &List{Items: make([]any, len(x.Items))}
			for j, item := range x.Items {
				l.Items[j] = changeDictValuesAt(item, rewrite, depth+1)
			}
			newV = l
		case string:
			if k == "$ref" {
				newV = rewrite(x)
			}
		}
		out.Set(k, newV)
	}
	return out
}

// appendNoDuplicates adds value to the list obj[key], creating the list if
// needed, unless it is already there.
func appendNoDuplicates(obj any, key string, value any) {
	m, ok := deref(obj).(*Map)
	if !ok {
		raise("TypeError", "'%s' object does not support item assignment", pyTypeName(obj))
	}
	if !m.Has(key) {
		m.Set(key, NewList())
	}
	cur, _ := m.Get(key)
	if pyContains(cur, value) {
		return
	}
	l, ok := deref(cur).(*List)
	if !ok {
		raise("AttributeError", "'%s' object has no attribute 'append'", pyTypeName(cur))
	}
	l.Items = append(l.Items, value)
}

func oneOf(types ...string) *Map {
	l := NewList()
	for _, t := range types {
		m := NewMap()
		m.Set("type", t)
		l.Items = append(l.Items, m)
	}
	out := NewMap()
	out.Set("oneOf", l)
	return out
}
