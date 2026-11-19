package convert

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Options mirror the Python tool's command-line flags, plus PythonCompat.
type Options struct {
	Output     string // directory to write schemas to
	Prefix     string // prefix for $refs to shared definitions (before OpenAPI 3.0)
	StandAlone bool   // inline every $ref so each file stands alone
	Expanded   bool   // name Kubernetes files kind-group-version.json
	Kubernetes bool   // apply the Kubernetes-specific changes
	Strict     bool   // add "additionalProperties": false

	// PythonCompat keeps the Python tool's bugs, for byte-identical output
	// in every case. Without it these are fixed:
	//
	//   - OpenAPI 3: $refs point at the files actually written (tag.json, not
	//     Tag.json), and --stand-alone inlines from the spec itself instead of
	//     from files that may not exist yet.
	//   - A top-level additionalProperties $ref isn't prefixed twice.
	//   - A schema that fails is not written, instead of left as an empty file.
	//   - --strict doesn't replace an existing top-level additionalProperties
	//     (which turned map types into "must be empty").
	//
	// None of these affect Kubernetes or OpenShift specs.
	PythonCompat bool

	// The Fix options change behaviour the Python tool has on purpose or by
	// accident, and that published schemas rely on, so each is opt-in. They
	// can't be combined with PythonCompat.
	FixParsing     bool // read JSON as JSON and YAML as YAML 1.2, not PyYAML's YAML 1.1
	FixRefSiblings bool // keep the description (and other annotations) next to an inlined $ref
	FixNullable    bool // don't make required top-level fields nullable (--kubernetes)
	FixKindNames   bool // when definitions share a file name, keep the best, not the last
}

func (o Options) anyFix() bool {
	return o.FixParsing || o.FixRefSiblings || o.FixNullable || o.FixKindNames
}

// Logger receives the same progress messages the Python tool prints.
type Logger interface {
	Info(msg string)  // stage headings
	Debug(msg string) // one pair per schema
	Error(msg string) // problems that skip a schema but don't stop the run
}

// These kinds embed JSON Schema themselves and can't be inlined.
var unsupportedStandAlone = []string{
	"jsonschemaprops",
	"jsonschemapropsorarray",
	"customresourcevalidation",
	"customresourcedefinition",
	"customresourcedefinitionspec",
	"customresourcedefinitionlist",
	"jsonschemapropsorstringarray",
	"jsonschemapropsorbool",
}

const schemasPointer = "#/components/schemas/"

// converter holds what one run needs while it writes the schemas.
type converter struct {
	opts    Options
	log     Logger
	version string
	loader  *Loader

	// baseURI is the output directory: before OpenAPI 3.0 (and in Python
	// compat mode) --stand-alone resolves $refs against files written there.
	baseURI string

	// OpenAPI 3 without PythonCompat: the file each definition is written
	// to, and a snapshot of the spec (taken before definitions are changed)
	// that --stand-alone inlines from.
	fileNames map[string]string
	specURI   string
	snapshot  any

	// FixKindNames: which definition each file written so far came from.
	written map[string]writtenFile
}

type writtenFile struct {
	title string
	rank  rank
}

// Run reads the spec at schema (a path or URL) and writes the JSON Schema
// files. Per-schema problems are logged and skipped, as in the Python tool;
// the returned error is for problems that stop the whole run.
func Run(schema string, opts Options, log Logger) (err error) {
	defer func() {
		if r := recover(); r != nil {
			pe, ok := r.(*PyError)
			if !ok {
				panic(r)
			}
			err = fmt.Errorf("%s: %s", pe.Class, pe.Msg)
		}
	}()

	if opts.PythonCompat && opts.anyFix() {
		return fmt.Errorf("--python-compat can't be combined with the --fix options")
	}

	log.Info("Downloading schema")
	raw, err := fetch(schema)
	if err != nil {
		return err
	}

	log.Info("Parsing schema")
	parsed, err := ParseSpec(raw, opts.FixParsing)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", schema, err)
	}
	data, ok := parsed.(*Map)
	if !ok {
		return fmt.Errorf("%s is not an OpenAPI document: top level is not an object", schema)
	}
	version, err := specVersion(data)
	if err != nil {
		return fmt.Errorf("%s: %w", schema, err)
	}

	if err := os.MkdirAll(opts.Output, 0o755); err != nil {
		return err
	}
	absOut, err := filepath.Abs(opts.Output)
	if err != nil {
		return err
	}
	c := &converter{
		opts:    opts,
		log:     log,
		version: version,
		loader:  NewLoader(),
		baseURI: (&url.URL{Scheme: "file", Path: absOut + "/"}).String(),
		written: map[string]writtenFile{},
	}

	if version < "3" {
		if err := c.writeSharedDefinitions(data); err != nil {
			return err
		}
	}

	log.Info("Generating individual schemas")
	var components *Map
	if version < "3" {
		components = asMap(pyGetItem(data, "definitions"), "definitions")
	} else {
		components = asMap(pyGetItem(pyGetItem(data, "components"), "schemas"), "components.schemas")
		if !opts.PythonCompat {
			if err := c.prepareOpenAPI3(schema, data, components); err != nil {
				return err
			}
		}
	}

	types := []string{}
	for _, title := range slices.Clone(components.Keys()) {
		parts, kind, fullName := c.names(title)

		specValue, _ := components.Get(title)
		rank := rankOf(parts, specValue)
		spec, ok := deref(specValue).(*Map)
		if !ok {
			raise("TypeError", "definition %s is a %s, not an object", pyRepr(title), pyTypeName(specValue))
		}
		spec.Set("$schema", "http://json-schema.org/schema#")
		spec.SetDefault("type", "object")
		if opts.Strict {
			if opts.PythonCompat {
				spec.Set("additionalProperties", false)
			} else {
				spec.SetDefault("additionalProperties", false)
			}
		}

		types = append(types, title)

		if e := catch(func() { c.writeSchema(spec, title, parts, kind, fullName, rank) }); e != nil {
			log.Error(fmt.Sprintf("An error occured processing %s: %s", kind, e.Msg))
		}
	}

	log.Info("Generating schema for all types")
	all := NewList()
	for _, title := range types {
		ref := NewMap()
		if version < "3" {
			ref.Set("$ref", opts.Prefix+"#/definitions/"+title)
		} else {
			ref.Set("$ref", c.rewriteRef(schemasPointer+title))
		}
		all.Items = append(all.Items, ref)
	}
	contents := NewMap()
	contents.Set("oneOf", all)
	return os.WriteFile(filepath.Join(opts.Output, "all.json"), Dumps(contents), 0o644)
}

// names splits a definition title and works out its file name: kind.json,
// or kind-group-version.json with --kubernetes --expanded.
func (c *converter) names(title string) (parts []string, kind, fullName string) {
	parts = strings.Split(title, ".")
	kind = strings.ToLower(parts[len(parts)-1])
	fullName = kind
	if c.opts.Kubernetes {
		if len(parts) < 3 {
			raise("IndexError", "list index out of range (definition %s has fewer than 3 dotted parts)", pyRepr(title))
		}
		group := strings.ToLower(parts[len(parts)-3])
		apiVersion := strings.ToLower(parts[len(parts)-2])
		if c.opts.Expanded {
			if group == "core" || group == "api" {
				fullName = kind + "-" + apiVersion
			} else {
				fullName = kind + "-" + group + "-" + apiVersion
			}
		}
	}
	return parts, kind, fullName
}

// prepareOpenAPI3 records each definition's file name, so $refs can point
// at it, and takes the snapshot --stand-alone inlines from. The snapshot is
// a JSON round trip, like the _definitions.json file used before 3.0.
func (c *converter) prepareOpenAPI3(schema string, data, components *Map) error {
	c.fileNames = map[string]string{}
	for _, title := range components.Keys() {
		// A title that can't be named fails later, in the main loop.
		catch(func() {
			_, _, fullName := c.names(title)
			c.fileNames[title] = fullName
		})
	}
	if !c.opts.StandAlone {
		return nil
	}
	uri, err := locationURI(schema)
	if err != nil {
		return err
	}
	c.specURI = uri
	c.snapshot, err = parseJSON(Dumps(data), jsonNumbers)
	return err
}

// locationURI turns a spec path or URL into an absolute URI to resolve
// $refs against.
func locationURI(schema string) (string, error) {
	if u, err := url.Parse(schema); err == nil && len(u.Scheme) > 1 {
		return schema, nil
	}
	abs, err := filepath.Abs(schema)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: abs}).String(), nil
}

// rewriteRef is what changeDictValues does to each string "$ref".
func (c *converter) rewriteRef(ref string) string {
	if c.version < "3" {
		return c.opts.Prefix + ref
	}
	if c.fileNames != nil {
		if title, ok := strings.CutPrefix(ref, schemasPointer); ok {
			if name, ok := c.fileNames[title]; ok {
				return name + ".json"
			}
		}
	}
	return strings.ReplaceAll(ref, schemasPointer, "") + ".json"
}

// specVersion returns the "swagger" or "openapi" field as a string. Python
// compares it to "3" as a string, so "2.0" and "3.0.3" sort as expected.
func specVersion(data *Map) (string, error) {
	v, ok := data.Get("swagger")
	if !ok {
		v, ok = data.Get("openapi")
	}
	if !ok {
		return "", fmt.Errorf("not an OpenAPI document: no \"swagger\" or \"openapi\" field")
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case Int:
		return string(x), nil
	case float64:
		return pyFloatRepr(x), nil
	}
	return "", fmt.Errorf("unexpected version field %v", v)
}

func asMap(v any, what string) *Map {
	m, ok := deref(v).(*Map)
	if !ok {
		raise("TypeError", "%s is a %s, not an object", what, pyTypeName(v))
	}
	return m
}

// writeSharedDefinitions writes _definitions.json, which the per-type files
// point at before OpenAPI 3.0. The Kubernetes and strict changes made here
// are made in place, so the per-type files see them too.
func (c *converter) writeSharedDefinitions(data *Map) error {
	opts := c.opts
	path := filepath.Join(opts.Output, "_definitions.json")
	if opts.PythonCompat {
		// Python opens (and so truncates) the file before anything else.
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return err
		}
	}
	c.log.Info("Generating shared definitions")
	definitions := asMap(pyGetItem(data, "definitions"), "definitions")

	if opts.Kubernetes {
		definitions.Set("io.k8s.apimachinery.pkg.util.intstr.IntOrString", oneOf("string", "integer"))
		// Kubernetes doesn't accept numbers for Quantity, but nearly all
		// Kubernetes tooling does, so allow them.
		definitions.Set("io.k8s.apimachinery.pkg.api.resource.Quantity", oneOf("string", "number"))

		// Fill in apiVersion and kind enums from x-kubernetes-group-version-kind.
		for _, typeName := range definitions.Keys() {
			typeDef, _ := definitions.Get(typeName)
			if !pyContains(typeDef, "properties") {
				c.log.Error(typeName + " has no properties")
				continue
			}
			if !pyContains(typeDef, "x-kubernetes-group-version-kind") {
				continue
			}
			props := pyGetItem(typeDef, "properties")
			for _, ext := range pyIter(pyGetItem(typeDef, "x-kubernetes-group-version-kind")) {
				if opts.Expanded && pyContains(props, "apiVersion") {
					group, version := pyGetItem(ext, "group"), pyGetItem(ext, "version")
					apiVersion := version
					if pyTruthy(group) {
						apiVersion = pyStrConcat(group, "/", version)
					}
					appendNoDuplicates(pyGetItem(props, "apiVersion"), "enum", apiVersion)
				}
				if pyContains(props, "kind") {
					appendNoDuplicates(pyGetItem(props, "kind"), "enum", pyGetItem(ext, "kind"))
				}
			}
		}
	}

	var defs any = definitions
	if opts.Strict {
		defs = additionalProperties(definitions)
	}
	out := NewMap()
	out.Set("definitions", defs)
	return os.WriteFile(path, Dumps(out), 0o644)
}

// pyStrConcat is Python's a + b + c for strings.
func pyStrConcat(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		s, ok := deref(p).(string)
		if !ok {
			raise("TypeError", "can only concatenate str (not \"%s\") to str", pyTypeName(p))
		}
		b.WriteString(s)
	}
	return b.String()
}

// writeSchema is the body of the Python tool's per-type try block.
func (c *converter) writeSchema(spec *Map, title string, parts []string, kind, fullName string, rank rank) {
	opts := c.opts
	c.log.Debug("Processing " + fullName)

	if opts.FixKindNames {
		if prev, ok := c.written[fullName]; ok && rank.less(prev.rank) {
			c.log.Debug(fmt.Sprintf("Keeping %s.json from %s", fullName, prev.title))
			return
		}
	}

	// These APIs are all deprecated.
	if opts.Kubernetes {
		if len(parts) < 4 {
			raise("IndexError", "list index out of range")
		}
		if parts[3] == "pkg" && parts[2] == "kubernetes" {
			raise("UnsupportedError", "%s not currently supported, due to use of pkg namespace", title)
		}
	}
	if opts.Kubernetes && opts.StandAlone && slices.Contains(unsupportedStandAlone, kind) {
		raise("UnsupportedError", "%s not currently supported", kind)
	}

	var specification any
	if c.snapshot != nil {
		// OpenAPI 3 --stand-alone: inline from the spec itself.
		specification = ReplaceRefsIn(spec, c.specURI, c.snapshot, c.loader, opts.FixRefSiblings)
	} else {
		specification = changeDictValues(spec, c.rewriteRef)
		if opts.StandAlone {
			specification = ReplaceRefs(specification, c.baseURI, c.loader, opts.FixRefSiblings)
		}
	}

	if opts.PythonCompat && pyContains(specification, "additionalProperties") {
		// The refs in here were already rewritten above, so this rewrites
		// them a second time.
		if ap := pyGetItem(specification, "additionalProperties"); pyTruthy(ap) {
			setItem(specification, "additionalProperties", changeDictValues(ap, c.rewriteRef))
		}
	}
	if opts.Strict && pyContains(specification, "properties") {
		setItem(specification, "properties", additionalProperties(pyGetItem(specification, "properties")))
	}
	if opts.Kubernetes && pyContains(specification, "properties") {
		updated := replaceIntOrString(pyGetItem(specification, "properties"))
		var schema any // nil, as in the Python: top-level fields are always nullable
		if opts.FixNullable {
			schema = specification
		}
		updated = allowNullOptionalFields(updated, schema)
		setItem(specification, "properties", updated)
	}

	path := filepath.Join(opts.Output, fullName+".json")
	if !opts.PythonCompat {
		c.log.Debug("Generating " + fullName + ".json")
		if err := os.WriteFile(path, Dumps(specification), 0o644); err != nil {
			raise("OSError", "%v", err)
		}
		c.written[fullName] = writtenFile{title, rank}
		return
	}
	// Python opens the file before serialising, so a failure leaves it empty.
	f, err := os.Create(path)
	if err != nil {
		raise("OSError", "%v", err)
	}
	defer f.Close()
	c.log.Debug("Generating " + fullName + ".json")
	if _, err := f.Write(Dumps(specification)); err != nil {
		raise("OSError", "%v", err)
	}
}

func setItem(container any, key string, v any) {
	m, ok := deref(container).(*Map)
	if !ok {
		raise("TypeError", "'%s' object does not support item assignment", pyTypeName(container))
	}
	m.Set(key, v)
}

// rank orders definitions that would be written to the same file, for
// FixKindNames. Without it the last one in the spec wins, so kind.json for
// a kind defined in several API groups or versions is whichever comes last.
type rank struct {
	resource     bool // has x-kubernetes-group-version-kind: a real API resource, not a helper type
	stability    int  // 3 stable (v1), 2 beta, 1 alpha, 0 not a version
	major, minor int  // v2beta1 is major 2, minor 1
	core         bool // the core group ("" in apiVersion: v1)
}

// less reports whether a should lose to b. Fields are compared in order, so
// a real resource beats a helper type, then a more stable version beats a
// less stable one (Kubernetes' version priority: v2 > v1 > v1beta2 >
// v1beta1 > v1alpha1), then the core group beats the others.
func (a rank) less(b rank) bool {
	if a.resource != b.resource {
		return !a.resource
	}
	if a.stability != b.stability {
		return a.stability < b.stability
	}
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return !a.core && b.core
}

var kubeVersion = regexp.MustCompile(`^v([0-9]+)(?:(alpha|beta)([0-9]+))?$`)

// rankOf ranks a definition from its title (…group.version.Kind) and
// whether it declares x-kubernetes-group-version-kind.
func rankOf(parts []string, def any) rank {
	var r rank
	if m, ok := def.(*Map); ok {
		r.resource = m.Has("x-kubernetes-group-version-kind")
	}
	if len(parts) >= 3 {
		group := strings.ToLower(parts[len(parts)-3])
		r.core = group == "core" || group == "api"
	}
	if len(parts) >= 2 {
		if m := kubeVersion.FindStringSubmatch(strings.ToLower(parts[len(parts)-2])); m != nil {
			r.major, _ = strconv.Atoi(m[1])
			switch m[2] {
			case "":
				r.stability = 3
			case "beta":
				r.stability = 2
			case "alpha":
				r.stability = 1
			}
			if m[3] != "" {
				r.minor, _ = strconv.Atoi(m[3])
			}
		}
	}
	return r
}
