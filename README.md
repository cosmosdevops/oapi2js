# oapi2js

Converts an OpenAPI specification (Swagger 2.0 or OpenAPI 3, JSON or YAML)
into a directory of JSON Schema files, one per definition.

a Go reimagining of
[instrumenta/openapi2jsonschema](https://github.com/instrumenta/openapi2jsonschema)
with a few Python oddities fixed (see below); the original behaviour of openapi2jsonschema can be preserved with `--python-compat` .
In my tests this is much faster, my `--stand-alone` passes over an OpenShift spec takes about 1.5 seconds.

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

### Fixed bugs compared to the original python

`--python-compat` should produce output that matches the Python tool. Otherwise fixes include:

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
`--python-compat`.).

- **`--fix-parsing`**: numbers and YAML values. Python reads the spec with
  YAML 1.1 rules, so the JSON number `1e3` becomes the *string* `"1e3"`, and
  in a YAML spec `yes`/`no`/`on`/`off` become booleans, `0755` is octal 493
  and `1:30` is 90. With the fix, JSON follows the JSON spec and YAML follows
  the YAML 1.2 core schema (as OpenAPI says): `1e3` is a number, only
  `true`/`false` are booleans, `0755` is 755 (`0o755` is octal) and `1:30`
  is a string. A YAML spec that writes `default: yes` for a boolean now gets
  the string `"yes"`. Floats are still written the way Python prints them
  (`1000.0`, `1e-05`). 
- **`--fix-ref-siblings`**: keep the description next to a `$ref`. When
  `--stand-alone` inlines `{"description": "...", "$ref": "..."}`, Python
  keeps only the target, so field descriptions are lost. The fix lays the
  annotations from next to the `$ref` over the inlined copy, and the field's
  own value override: `description`, `title`, `default`, `example`, `examples`,
  `deprecated`, `readOnly`, `writeOnly`, `$comment`, `externalDocs` and any
  `x-` extension. Keywords that change validation (`type`, `properties` and
  so on) are still ignored, as OpenAPI 2 and 3.0 require.
- **`--fix-nullable`**: required top-level fields. With `--kubernetes`,
  fields that aren't required accept `null`. Python checks `required` for
  nested fields but not top-level ones, so `spec: null` passes for a Route
  even though `spec` is required. The fix checks the top level the same way.
  Changes 1346 files. Manifests that set a required top-level field to
  `null` will start failing.
- **`--fix-kind-names`**: Without
  `--expanded`, definitions that share a kind share a file, and Python keeps
  the last one in the spec. This fix try's to keep the best one that was written
  successfully: a real API resource (`x-kubernetes-group-version-kind`) over a
  helper type; then the most stable and newest version (v2 > v1 > v1beta2 >
  v1beta1 > v1alpha1); then the core group. a complete tie still go to the last one.
  
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
