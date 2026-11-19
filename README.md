# oapi2js

Converts an OpenAPI specification (Swagger 2.0 or OpenAPI 3, JSON or YAML)
into a directory of JSON Schema files, one per definition.

It is a Go port of
[instrumenta/openapi2jsonschema](https://github.com/instrumenta/openapi2jsonschema)
(the version pinned by openshift-json-schema, plus its `no-properties.patch`).
It takes the same flags and, for Kubernetes and OpenShift specs, writes
**byte-identical output**, so tools like kubeconform see no difference. A few
Python bugs are fixed (see below); `--python-compat` keeps them. It is much
faster: a `--stand-alone` pass over an OpenShift spec takes about 1.5 seconds
instead of about 10 minutes.

## Usage

```sh
go install github.com/cosmosdevops/oapi2js@latest   # or, in a checkout: go build .
oapi2js [OPTIONS] SCHEMA_URL
```

`SCHEMA_URL` is a file path, a `file://` URI or an `http(s)` URL.

| Option | Meaning |
| --- | --- |
| `-o, --output PATH` | Directory to write the schemas to (default `schemas`) |
| `-p, --prefix TEXT` | Prefix for `$ref`s to the shared definitions file, before OpenAPI 3.0 (default `_definitions.json`) |
| `--stand-alone` | Inline every `$ref`, so each file works on its own |
| `--expanded` | Name Kubernetes files `kind-group-version.json` instead of `kind.json` |
| `--kubernetes` | Apply the Kubernetes changes (see below) |
| `--strict` | Add `"additionalProperties": false`, so unknown fields are rejected |
| `--python-compat` | Reproduce the Python tool's output exactly, bugs included |
| `--fix-parsing` | Read JSON with JSON rules and YAML with YAML 1.2 rules (see below) |
| `--fix-ref-siblings` | Keep the description next to a `$ref` when inlining it |
| `--fix-nullable` | With `--kubernetes`, don't let required top-level fields be null |
| `--fix-kind-names` | When definitions share a file name, keep the best one, not the last |
| `--fix-all` | All of the `--fix` options |

Options can come before or after `SCHEMA_URL`.

### Kubernetes / OpenShift schemas

This builds the same layout as kubernetes-json-schema and
openshift-json-schema: `kind-group-version.json` and `kind.json` in one
directory, from two runs.

```sh
for expanded in --expanded ""; do
  oapi2js -o v5.0-standalone-strict --kubernetes $expanded \
    --stand-alone --strict openapi.json
done
```

With `--kubernetes` the tool:

- fills in `apiVersion` and `kind` enums from `x-kubernetes-group-version-kind`;
- lets `IntOrString` and `int-or-string` fields be a string or an integer, and
  `Quantity` be a string or a number;
- makes fields that aren't required accept `null`;
- skips the old `io.k8s.kubernetes.pkg.*` types, and with `--stand-alone`
  the CRD and `JSONSchemaProps` types, which can't be inlined.

### Docker

The image is a drop-in replacement for one built from the Python tool: same
arguments, and the working directory is `/out`.

```sh
docker build -t oapi2js .
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/out" oapi2js \
  -o schemas --kubernetes --stand-alone openapi.json
```

## Differences from the Python tool

### Fixed bugs

These are fixed by default. `--python-compat` brings them back, for output
that matches the Python tool byte for byte in every case. None of them
affect Kubernetes or OpenShift specs: v4.12 and v5.0 come out identical
either way.

- **OpenAPI 3 refs point at the wrong files.** Python rewrites
  `#/components/schemas/Tag` to `Tag.json` but writes `tag.json`, so the
  refs are broken on a case-sensitive filesystem. Refs now use the real file
  name (including `kind-group-version.json` names with `--kubernetes
  --expanded`), in each schema and in `all.json`.
- **OpenAPI 3 `--stand-alone` produces empty files.** Python inlines by
  reading the other schemas' files from disk, which fails when the name's
  case is wrong or the file hasn't been written yet. Refs are now inlined
  from the spec itself, the same way Swagger 2.0 inlines from
  `_definitions.json`.
- **Top-level `additionalProperties` refs get the prefix twice** without
  `--stand-alone` (`_definitions.json_definitions.json#/...`). Now once.
- **A schema that fails is left as an empty file.** Python opens the file
  before writing it. Now a failed schema is logged and not written.
- **`--strict` turns map types into "must be empty".** Python replaces a
  definition's own `additionalProperties` (for example
  `{"$ref": "#/definitions/Tag"}`) with `false`. Now `false` is only added
  when `additionalProperties` isn't already set.

### Opt-in fixes

These are off by default, because they change published Kubernetes schemas
(or, for `--fix-parsing`, YAML specs). They can't be combined with
`--python-compat`. Numbers below are for the v5.0 OpenShift schemas
(`--stand-alone --strict`, 2050 files).

- **`--fix-parsing`**: numbers and YAML values. Python reads the spec with
  YAML 1.1 rules, so the JSON number `1e3` becomes the *string* `"1e3"`, and
  in a YAML spec `yes`/`no`/`on`/`off` become booleans, `0755` is octal 493
  and `1:30` is 90. With the fix, JSON follows the JSON spec and YAML follows
  the YAML 1.2 core schema (as OpenAPI says): `1e3` is a number, only
  `true`/`false` are booleans, `0755` is 755 (`0o755` is octal) and `1:30`
  is a string. A YAML spec that writes `default: yes` for a boolean now gets
  the string `"yes"`. Floats are still written the way Python prints them
  (`1000.0`, `1e-05`). Changes no OpenShift files: their specs only have
  whole numbers.
- **`--fix-ref-siblings`**: keep the description next to a `$ref`. When
  `--stand-alone` inlines `{"description": "...", "$ref": "..."}`, Python
  keeps only the target, so field descriptions are lost. The fix lays the
  annotations from next to the `$ref` over the inlined copy, and the field's
  own value wins: `description`, `title`, `default`, `example`, `examples`,
  `deprecated`, `readOnly`, `writeOnly`, `$comment`, `externalDocs` and any
  `x-` extension. Keywords that change validation (`type`, `properties` and
  so on) are still ignored, as JSON Schema draft 4 to 7 and OpenAPI 2 and
  3.0 require. A definition that is only a `$ref` also keeps its `$schema`
  line. Changes 1538 files, mostly a better `metadata` description.
- **`--fix-nullable`**: required top-level fields. With `--kubernetes`,
  fields that aren't required accept `null`. Python checks `required` for
  nested fields but not top-level ones, so `spec: null` passes for a Route
  even though `spec` is required. The fix checks the top level the same way.
  Changes 1346 files. Manifests that set a required top-level field to
  `null` will start failing.
- **`--fix-kind-names`**: which definition `kind.json` comes from. Without
  `--expanded`, definitions that share a kind share a file, and Python keeps
  the last one in the spec. The fix keeps the best one that was written
  successfully: a real API resource (`x-kubernetes-group-version-kind`) over a
  helper type; then the most stable and newest version (v2 > v1 > v1beta2 >
  v1beta1 > v1alpha1); then the core group. Ties still go to the last one.
  Changes 23 files, for example `event.json` (now core v1, was
  `events.k8s.io/v1`), `node.json` (now the core Node, was OpenShift's
  `config.openshift.io` Node), `probe.json` (now the Prometheus `Probe`
  resource, was the liveness probe struct) and the Gateway API kinds (now v1,
  were v1beta1). `kind-group-version.json` files don't change.

### Kept as they are

- **Array items and map values accept `null`** with `--kubernetes` (for
  example each entry in `containers`, or a label value), because the
  `required` check only applies to named fields. `--fix-nullable` doesn't
  change this.
- **OpenAPI 3 refs to a definition that lost a file-name clash** point at the
  file that was kept, since both have the same name.

### Other small differences

None of these change the output for valid specs:

- `-o` can be an absolute path (the Python tool needed a relative one for
  `--stand-alone`).
- A YAML spec with an unquoted date stays a string. The Python tool fails on
  it.
- Fatal errors are a one-line message, not a Python traceback.

## How it works

The code is in `internal/convert`:

- `value.go`: the data model. Maps keep insertion order like Python dicts,
  and maps and lists are shared by pointer, because the Python code changes
  objects in place and relies on those changes being seen elsewhere.
- `decode.go`: JSON and YAML parsing with PyYAML's rules.
- `encode.go`: writes JSON exactly like Python's `json.dumps(indent=2)`.
- `ref.go`: `$ref` inlining, copying how jsonref 0.2 behaves. jsonref
  deep-copies the whole definitions file for every schema, which is why the
  Python tool is slow. Here only the parts a schema actually reaches are
  copied, which gives the same result.
- `util.go`, `convert.go`: the conversion steps, ported line by line.

## Testing

```sh
go test ./...
```

`testdata/cases.txt` lists fixture specs and flag combinations. Each case
is checked twice:

- `testdata/golden/` holds what the Python tool writes. With
  `--python-compat` the Go output must match it byte for byte. Cases with
  `--fix` options are skipped, as Python has no equivalent.
- `testdata/golden-fixed/` holds the default (fixed) output. Run
  `diff -r testdata/golden testdata/golden-fixed` to see exactly what the
  fixes change. After a deliberate change, regenerate it with
  `go test ./internal/convert -run TestFixed -update` and review the diff.

To regenerate the Python golden files (needs Docker):

```sh
scripts/update-golden
```

To compare both tools on any spec (also needs Docker; it runs this tool
with `--python-compat`):

```sh
scripts/compare openapi.json --kubernetes --stand-alone --strict --expanded
```

`testdata/python/` holds the Dockerfile and patch for the Python reference
tool, copied from openshift-json-schema's `automation/generator`.
