package convert

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// This file reproduces the parts of jsonref 0.2 the Python tool relies on
// for --stand-alone.
//
// jsonref.replace_refs deep-copies a schema, swapping every object with a
// string "$ref" for a lazy proxy (siblings of "$ref" are dropped). When a
// proxy is first touched it loads the target document, deep-copies that
// document the same way into a store shared by everything reachable from
// the one schema, and follows the JSON pointer. Changes made through a proxy
// land in that per-schema copy, so they are visible to the rest of the same
// schema but never leak into the next one.
//
// Copying all of _definitions.json for every schema is why the Python tool
// takes minutes. Here a document is copied lazily: a container is copied the
// first time anything reaches it, and the copy is remembered, so each schema
// sees exactly what it would see with a full copy.

// Ref is a lazily resolved JSON reference: Python's JsonRef proxy.
type Ref struct {
	obj *Map     // the original {"$ref": ...} object
	doc *lazyDoc // the document it appears in, which $ref is relative to

	resolving bool
	done      bool
	subject   any
}

// refStore maps document URIs to their per-schema copies.
type refStore struct {
	docs   map[string]*lazyDoc
	loader *Loader

	// keepSiblings keeps annotations written next to a "$ref" (see
	// Ref.withSiblings). jsonref drops them.
	keepSiblings bool
}

func newRefStore(loader *Loader, keepSiblings bool) *refStore {
	return &refStore{docs: map[string]*lazyDoc{}, loader: loader, keepSiblings: keepSiblings}
}

// lazyDoc is one document as seen from one schema: the raw parsed document
// plus the copies made of its containers so far.
type lazyDoc struct {
	raw    any
	uri    string
	store  *refStore
	copies map[any]any // raw *Map / *List -> its copy (*Map, *List or *Ref)
}

func (s *refStore) newDoc(raw any, uri string) *lazyDoc {
	return &lazyDoc{raw: raw, uri: uri, store: s, copies: map[any]any{}}
}

func isRefObject(m *Map) bool {
	v, ok := m.Get("$ref")
	if !ok {
		return false
	}
	_, isStr := v.(string)
	return isStr
}

// copy returns the per-schema copy of a raw value, making it on first use.
func (d *lazyDoc) copy(raw any) any {
	switch x := raw.(type) {
	case *Map:
		if c, ok := d.copies[x]; ok {
			return c
		}
		if isRefObject(x) {
			r := &Ref{obj: x, doc: d}
			d.copies[x] = r
			return r
		}
		m := NewMap()
		d.copies[x] = m
		for i, k := range x.keys {
			m.Set(k, d.copy(x.vals[i]))
		}
		return m
	case *List:
		if c, ok := d.copies[x]; ok {
			return c
		}
		l := &List{Items: make([]any, len(x.Items))}
		d.copies[x] = l
		for i, item := range x.Items {
			l.Items[i] = d.copy(item)
		}
		return l
	}
	return raw
}

// ReplaceRefs is jsonref's JsonRef.replace_refs(obj, base_uri=baseURI) with
// a fresh store. The result may itself be a *Ref.
func ReplaceRefs(obj any, baseURI string, loader *Loader, keepSiblings bool) any {
	store := newRefStore(loader, keepSiblings)
	uri, _ := urlDefrag(baseURI)
	d := store.newDoc(obj, uri)
	out := d.copy(obj)
	if _, isRef := out.(*Ref); !isRef {
		store.docs[uri] = d
	}
	return out
}

// ReplaceRefsIn is like ReplaceRefs, but refs in obj resolve against doc,
// the whole document obj came from (registered under docURI), rather than
// against obj itself. Used to inline OpenAPI 3 refs from the spec.
func ReplaceRefsIn(obj any, docURI string, doc any, loader *Loader, keepSiblings bool) any {
	store := newRefStore(loader, keepSiblings)
	uri, _ := urlDefrag(docURI)
	store.docs[uri] = store.newDoc(doc, uri)
	return store.newDoc(obj, uri).copy(obj)
}

// Subject resolves the reference (once) and returns what it points at.
func (r *Ref) Subject() any {
	if r.done {
		return r.subject
	}
	if r.resolving {
		raise("RecursionError", "maximum recursion depth exceeded")
	}
	r.resolving = true
	defer func() { r.resolving = false }()

	store := r.doc.store
	uri, fragment := urlDefrag(r.fullURI())
	doc, ok := store.docs[uri]
	if !ok {
		raw, err := store.loader.Load(uri)
		if err != nil {
			r.fail("%s", err)
		}
		doc = store.newDoc(raw, uri)
		if m, isMap := raw.(*Map); !isMap || !isRefObject(m) {
			store.docs[uri] = doc
		}
	}
	result := doc.resolvePointer(fragment, func(msg string) { r.fail("%s", msg) })
	if inner, ok := result.(*Ref); ok {
		result = inner.Subject()
	}
	if store.keepSiblings {
		result = r.withSiblings(result)
	}
	r.subject, r.done = result, true
	return result
}

func (r *Ref) fullURI() string {
	ref, _ := r.obj.Get("$ref")
	return urlJoin(r.doc.uri, ref.(string))
}

// siblingAnnotations are the keys next to a "$ref" that withSiblings keeps:
// ones that describe a value without changing what is valid. Keywords that
// do change validation are still ignored, as JSON Schema draft 4 to 7 and
// OpenAPI 2 and 3.0 say. (That also protects against the "type": "object"
// this tool adds to every definition, which would otherwise override the
// type of a definition that is only a "$ref".)
var siblingAnnotations = map[string]bool{
	"description":  true,
	"title":        true,
	"default":      true,
	"example":      true,
	"examples":     true,
	"deprecated":   true,
	"readOnly":     true,
	"writeOnly":    true,
	"$comment":     true,
	"externalDocs": true,
}

// withSiblings returns target with the annotations from next to the "$ref"
// laid over it: the field's own description beats the type's. Extensions
// ("x-...") count as annotations, and "$schema" is added only if missing,
// so a definition that is only a "$ref" keeps its "$schema" line.
//
// The result is a shallow copy, so other references to the same target
// don't pick up this field's description, while changes made further down
// (such as --strict adding additionalProperties) are still shared.
func (r *Ref) withSiblings(target any) any {
	m, ok := target.(*Map)
	if !ok {
		return target
	}
	var merged *Map
	for i, k := range r.obj.keys {
		keep := siblingAnnotations[k] || strings.HasPrefix(k, "x-") || (k == "$schema" && !m.Has(k))
		if !keep {
			continue
		}
		if merged == nil {
			merged = m.shallowCopy()
		}
		merged.Set(k, r.doc.copy(r.obj.vals[i]))
	}
	if merged == nil {
		return target
	}
	return merged
}

func (r *Ref) fail(format string, args ...any) {
	raise("JsonRefError", format, args...)
}

var pointerEscape = regexp.MustCompile(`\^/|\^\^`)

// splitPointer is jsonref's pointer parsing: unquote, split on "/" not
// preceded by "^", then turn "^/" into "/" and "^^" into "^".
func splitPointer(pointer string) []string {
	if pointer == "" {
		return nil
	}
	s := pyUnquote(strings.TrimLeft(pointer, "/"))
	// Go's regexp has no lookbehind, so split by hand.
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' && (i == 0 || s[i-1] != '^') {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	for i, p := range parts {
		parts[i] = pointerEscape.ReplaceAllStringFunc(p, func(m string) string { return m[1:] })
	}
	return parts
}

// resolvePointer finds the value at a JSON pointer, as jsonref does on its
// fully copied document. It walks the raw document while nothing on the
// path has been copied yet, and switches to the copy as soon as a container
// has one, so earlier changes made through proxies are seen.
func (d *lazyDoc) resolvePointer(pointer string, fail func(string)) any {
	cur := d.raw
	inCopy := false
	for _, part := range splitPointer(pointer) {
		if !inCopy {
			if c, ok := d.copies[cur]; ok {
				cur, inCopy = c, true
			} else if m, ok := cur.(*Map); ok && isRefObject(m) {
				cur, inCopy = d.copy(m), true
			}
		}
		if inCopy {
			cur = deref(cur)
		}
		next, ok := index(cur, part)
		if !ok {
			fail("Unresolvable JSON pointer: " + pyRepr(pointer))
		}
		cur = next
	}
	if !inCopy {
		cur = d.copy(cur)
	}
	return cur
}

// index is document[part] in jsonref's resolve_pointer.
func index(doc any, part string) (any, bool) {
	switch x := doc.(type) {
	case *Map:
		return x.Get(part)
	case *List:
		i, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		if i < 0 {
			i += len(x.Items)
		}
		if i < 0 || i >= len(x.Items) {
			return nil, false
		}
		return x.Items[i], true
	case string:
		rs := []rune(x)
		i, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		if i < 0 {
			i += len(rs)
		}
		if i < 0 || i >= len(rs) {
			return nil, false
		}
		return string(rs[i]), true
	}
	return nil, false
}

// Loader fetches and parses referenced documents, caching them for the
// life of the process like jsonref's global loader. Documents are parsed as
// JSON with Python's json module rules.
type Loader struct {
	mu    sync.Mutex
	cache map[string]any
}

func NewLoader() *Loader { return &Loader{cache: map[string]any{}} }

func (l *Loader) Load(uri string) (any, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, ok := l.cache[uri]; ok {
		return v, nil
	}
	data, err := fetch(uri)
	if err != nil {
		return nil, err
	}
	v, err := parseJSON(data, jsonNumbers)
	if err != nil {
		return nil, fmt.Errorf("JSONDecodeError: %s: %v", uri, err)
	}
	l.cache[uri] = v
	return v, nil
}

// fetch reads a file path, file:// URI or http(s) URL.
func fetch(location string) ([]byte, error) {
	u, err := url.Parse(location)
	if err != nil || u.Scheme == "" || len(u.Scheme) == 1 { // no scheme, or a Windows drive letter
		return os.ReadFile(location)
	}
	switch u.Scheme {
	case "file":
		data, err := os.ReadFile(u.Path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("URLError: <urlopen error [Errno 2] No such file or directory: %s>", pyRepr(u.Path))
		} else if err != nil {
			return nil, fmt.Errorf("URLError: <urlopen error %v>", err)
		}
		return data, nil
	case "http", "https":
		resp, err := http.Get(location)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTPError: %s: %s", location, resp.Status)
		}
		return io.ReadAll(resp.Body)
	}
	return nil, fmt.Errorf("URLError: unknown url type: %s", u.Scheme)
}

// urlJoin is Python's urllib.parse.urljoin for the cases jsonref meets.
func urlJoin(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// urlDefrag splits off the fragment, keeping it percent-encoded like
// Python's urldefrag.
func urlDefrag(s string) (string, string) {
	before, frag, _ := strings.Cut(s, "#")
	return before, frag
}

// pyUnquote is urllib.parse.unquote: decode %XX, leave bad escapes alone.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
