package convert

import (
	"bufio"
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type nopLogger struct{}

func (nopLogger) Info(string)  {}
func (nopLogger) Debug(string) {}
func (nopLogger) Error(string) {}

var update = flag.Bool("update", false, "rewrite testdata/golden-fixed from the current output")

// TestGolden runs every case in testdata/cases.txt with PythonCompat and
// checks the output is byte-identical to what the Python tool wrote
// (testdata/golden).
func TestGolden(t *testing.T) {
	runCases(t, true, "golden")
}

// TestFixed runs every case without PythonCompat and checks the output
// against testdata/golden-fixed, which is reviewed by hand: diff it against
// testdata/golden to see exactly what the fixes change. Regenerate it with
// go test ./internal/convert -run TestFixed -update.
func TestFixed(t *testing.T) {
	runCases(t, false, "golden-fixed")
}

func runCases(t *testing.T, compat bool, goldenDir string) {
	root := filepath.Join("..", "..", "testdata")
	f, err := os.Open(filepath.Join(root, "cases.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		name, fixture, flags := fields[0], fields[1], fields[2:]
		if compat && slices.ContainsFunc(flags, func(f string) bool { return strings.HasPrefix(f, "--fix-") }) {
			continue // the Python tool has no equivalent
		}
		t.Run(name, func(t *testing.T) {
			opts := Options{Output: t.TempDir(), Prefix: "_definitions.json", PythonCompat: compat}
			for i := 0; i < len(flags); i++ {
				switch flags[i] {
				case "--stand-alone":
					opts.StandAlone = true
				case "--expanded":
					opts.Expanded = true
				case "--kubernetes":
					opts.Kubernetes = true
				case "--strict":
					opts.Strict = true
				case "--fix-parsing":
					opts.FixParsing = true
				case "--fix-ref-siblings":
					opts.FixRefSiblings = true
				case "--fix-nullable":
					opts.FixNullable = true
				case "--fix-kind-names":
					opts.FixKindNames = true
				case "--fix-all":
					opts.FixParsing, opts.FixRefSiblings, opts.FixNullable, opts.FixKindNames = true, true, true, true
				case "--prefix":
					i++
					opts.Prefix = flags[i]
				default:
					t.Fatalf("unknown flag %s in cases.txt", flags[i])
				}
			}
			if err := Run(filepath.Join(root, "fixtures", fixture), opts, nopLogger{}); err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(root, goldenDir, name)
			if *update && !compat {
				if err := os.RemoveAll(want); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(want, os.DirFS(opts.Output)); err != nil {
					t.Fatal(err)
				}
			}
			compareDirs(t, want, opts.Output)
		})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func compareDirs(t *testing.T, want, got string) {
	t.Helper()
	wantFiles, gotFiles := readDir(t, want), readDir(t, got)
	for name, w := range wantFiles {
		g, ok := gotFiles[name]
		if !ok {
			t.Errorf("%s: missing", name)
			continue
		}
		if !bytes.Equal(w, g) {
			t.Errorf("%s: differs\n%s", name, firstDifference(w, g))
		}
	}
	for name := range gotFiles {
		if _, ok := wantFiles[name]; !ok {
			t.Errorf("%s: unexpected file", name)
		}
	}
}

func readDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		files[rel] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func firstDifference(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return "line " + strconv.Itoa(i+1) + ":\n  want: " + w + "\n  got:  " + g
		}
	}
	return ""
}
