package convert

import (
	"math"
	"testing"
)

func TestPyFloatRepr(t *testing.T) {
	// Expected values are Python's repr().
	for f, want := range map[float64]string{
		0:                   "0.0",
		1:                   "1.0",
		-1.5:                "-1.5",
		0.1:                 "0.1",
		0.0001:              "0.0001",
		0.00001:             "1e-05",
		123.456:             "123.456",
		1e15:                "1000000000000000.0",
		1e16:                "1e+16",
		1.5e16:              "1.5e+16",
		12345678901234567.0: "1.2345678901234568e+16",
		1e100:               "1e+100",
		5e-324:              "5e-324",
		math.MaxFloat64:     "1.7976931348623157e+308",
		2.5e-5:              "2.5e-05",
		100:                 "100.0",
		0.30000000000000004: "0.30000000000000004",
	} {
		if got := pyFloatRepr(f); got != want {
			t.Errorf("pyFloatRepr(%v) = %s, want %s", f, got, want)
		}
	}
	if got := pyFloatRepr(math.Copysign(0, -1)); got != "-0.0" {
		t.Errorf("negative zero = %s", got)
	}
}

func TestDumps(t *testing.T) {
	m := NewMap()
	m.Set("s", "a\"b\\c\n\t\x01\x7fé😀/<>&")
	m.Set("empty", NewMap())
	m.Set("list", NewList(Int("1"), 2.0, true, nil, NewList()))
	want := `{
  "s": "a\"b\\c\n\t\u0001\u007f\u00e9\ud83d\ude00/<>&",
  "empty": {},
  "list": [
    1,
    2.0,
    true,
    null,
    []
  ]
}`
	if got := string(Dumps(m)); got != want {
		t.Errorf("Dumps =\n%s\nwant\n%s", got, want)
	}
}

func TestParseSpecNumbers(t *testing.T) {
	// PyYAML (YAML 1.1) rules for bare numbers in JSON.
	numbers := []byte(`[1, -0, 1.5, 1.5e+3, 1e3, 1.5e3, 1E-2]`)
	checkItems(t, numbers, false, Int("1"), Int("0"), 1.5, 1500.0, "1e3", "1.5e3", "1E-2")
	// --fix-parsing: JSON rules.
	checkItems(t, numbers, true, Int("1"), Int("0"), 1.5, 1500.0, 1000.0, 1500.0, 0.01)
}

func TestParseSpecYAML(t *testing.T) {
	doc := []byte("[yes, off, 0755, 0o755, 0x1F, '1:30', 1:30, 1e3, 1.5e+3, ~, true]")
	// PyYAML (YAML 1.1).
	checkItems(t, doc, false, true, false, Int("493"), "0o755", Int("31"), "1:30", Int("90"), "1e3", 1500.0, nil, true)
	// --fix-parsing: YAML 1.2 core schema.
	checkItems(t, doc, true, "yes", "off", Int("755"), Int("493"), Int("31"), "1:30", "1:30", 1000.0, 1500.0, nil, true)
}

func checkItems(t *testing.T, doc []byte, modern bool, want ...any) {
	t.Helper()
	v, err := ParseSpec(doc, modern)
	if err != nil {
		t.Fatal(err)
	}
	got := v.(*List).Items
	if len(got) != len(want) {
		t.Fatalf("modern=%v: got %d items, want %d", modern, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("modern=%v: item %d = %#v, want %#v", modern, i, got[i], want[i])
		}
	}
}

func TestKeyOrderAndReassign(t *testing.T) {
	m := NewMap()
	m.Set("a", 1.0)
	m.Set("b", 2.0)
	m.Set("a", 3.0) // keeps its place, like a Python dict
	m.SetDefault("b", 9.0)
	m.SetDefault("c", 4.0)
	if got := string(Dumps(m)); got != "{\n  \"a\": 3.0,\n  \"b\": 2.0,\n  \"c\": 4.0\n}" {
		t.Errorf("got %s", got)
	}
}
