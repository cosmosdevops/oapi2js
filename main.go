// Command oapi2js converts an OpenAPI specification into a set of
// JSON Schema files. It is a Go port of instrumenta/openapi2jsonschema with
// the same flags and output, minus a few bugs (--python-compat keeps them).
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/cosmosdevops/oapi2js/internal/convert"
)

const usage = `Usage: oapi2js [OPTIONS] SCHEMA_URL

  Converts a valid OpenAPI specification into a set of JSON Schema files.
  SCHEMA_URL is a file path, a file:// URI or an http(s) URL, in JSON or YAML.

Options:
  -o, --output PATH  Directory to store schema files  [default: schemas]
  -p, --prefix TEXT  Prefix for JSON references (only for OpenAPI versions
                     before 3.0)  [default: _definitions.json]
  --stand-alone      Whether or not to de-reference JSON schemas
  --expanded         Expand Kubernetes schemas by API version
  --kubernetes       Enable Kubernetes specific processors
  --strict           Prohibits properties not in the schema
                     (additionalProperties: false)
  --python-compat    Reproduce the Python tool's output exactly, bugs
                     included
  --version          Show the version and exit
  --help             Show this message and exit

Opt-in fixes (off by default because they change the output compared to
the Python tool; can't be combined with --python-compat):
  --fix-parsing      Read JSON with JSON rules and YAML with YAML 1.2 rules
                     instead of PyYAML's YAML 1.1 rules (1e3 is a number;
                     yes/no/on/off are strings; 0755 is decimal)
  --fix-ref-siblings Keep the description (and other annotations) written
                     next to a $ref when inlining it with --stand-alone
  --fix-nullable     With --kubernetes, don't let required top-level fields
                     be null
  --fix-kind-names   When definitions share a file name, keep the best one
                     (an API resource, the most stable and newest version,
                     the core group) instead of the last one in the spec
  --fix-all          All of the --fix options
`

func main() {
	opts, schema, err := parseArgs(os.Args[1:])
	if err == errHelp {
		fmt.Print(usage)
		return
	}
	if err == errVersion {
		fmt.Println("oapi2js", version())
		return
	}
	if err != nil {
		fmt.Fprint(os.Stderr, usage)
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		os.Exit(2)
	}

	log := newLogger(os.Stdout)
	if err := convert.Run(schema, opts, log); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

var (
	errHelp    = fmt.Errorf("help requested")
	errVersion = fmt.Errorf("version requested")
)

// parseArgs accepts options before or after SCHEMA_URL, in the forms click
// accepts: "-o DIR", "-oDIR", "--output DIR" and "--output=DIR".
func parseArgs(args []string) (convert.Options, string, error) {
	opts := convert.Options{Output: "schemas", Prefix: "_definitions.json"}
	bools := map[string]*bool{
		"--stand-alone":      &opts.StandAlone,
		"--expanded":         &opts.Expanded,
		"--kubernetes":       &opts.Kubernetes,
		"--strict":           &opts.Strict,
		"--python-compat":    &opts.PythonCompat,
		"--fix-parsing":      &opts.FixParsing,
		"--fix-ref-siblings": &opts.FixRefSiblings,
		"--fix-nullable":     &opts.FixNullable,
		"--fix-kind-names":   &opts.FixKindNames,
	}
	values := map[string]*string{
		"-o": &opts.Output, "--output": &opts.Output,
		"-p": &opts.Prefix, "--prefix": &opts.Prefix,
	}

	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case a == "--help" || a == "-h":
			return opts, "", errHelp
		case a == "--version":
			return opts, "", errVersion
		case a == "--fix-all":
			opts.FixParsing, opts.FixRefSiblings, opts.FixNullable, opts.FixKindNames = true, true, true, true
		case bools[a] != nil:
			*bools[a] = true
		case values[a] != nil:
			if i+1 >= len(args) {
				return opts, "", fmt.Errorf("option %s requires an argument", a)
			}
			i++
			*values[a] = args[i]
		case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			name, val, _ := strings.Cut(a, "=")
			if values[name] == nil {
				return opts, "", fmt.Errorf("no such option: %s", name)
			}
			*values[name] = val
		case len(a) > 2 && (strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "-p")):
			*values[a[:2]] = a[2:]
		case strings.HasPrefix(a, "-") && a != "-":
			return opts, "", fmt.Errorf("no such option: %s", a)
		default:
			positional = append(positional, a)
		}
	}
	if opts.PythonCompat && (opts.FixParsing || opts.FixRefSiblings || opts.FixNullable || opts.FixKindNames) {
		return opts, "", fmt.Errorf("--python-compat can't be combined with the --fix options")
	}
	switch len(positional) {
	case 0:
		return opts, "", fmt.Errorf("missing argument SCHEMA_URL")
	case 1:
		return opts, positional[0], nil
	default:
		return opts, "", fmt.Errorf("got unexpected extra argument (%s)", positional[1])
	}
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

// logger prints in green, yellow and red like the Python tool, and drops the
// colours when stdout isn't a terminal (as click does).
type logger struct {
	w     io.Writer
	color bool
}

func newLogger(f *os.File) *logger {
	st, err := f.Stat()
	return &logger{w: f, color: err == nil && st.Mode()&os.ModeCharDevice != 0}
}

func (l *logger) print(code, msg string) {
	if l.color {
		fmt.Fprintf(l.w, "\x1b[%sm%s\x1b[0m\n", code, msg)
	} else {
		fmt.Fprintln(l.w, msg)
	}
}

func (l *logger) Info(msg string)  { l.print("32", msg) }
func (l *logger) Debug(msg string) { l.print("33", msg) }
func (l *logger) Error(msg string) { l.print("31", msg) }
