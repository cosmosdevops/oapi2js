package convert

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Dumps serialises v exactly like Python's json.dumps(v, indent=2): ", " and
// ": " separators, non-ASCII escaped as \uXXXX, floats written as repr()
// does, and no trailing newline. *Ref values are written as what they point
// at. A reference cycle raises ValueError, as Python's encoder does.
func Dumps(v any) []byte {
	e := &encoder{active: map[any]bool{}}
	e.value(v, 0)
	return e.buf.Bytes()
}

type encoder struct {
	buf    bytes.Buffer
	active map[any]bool // containers currently being written, to spot cycles
}

func (e *encoder) enter(c any) {
	if e.active[c] {
		raise("ValueError", "Circular reference detected")
	}
	e.active[c] = true
}

func (e *encoder) newline(level int) {
	e.buf.WriteByte('\n')
	for i := 0; i < level; i++ {
		e.buf.WriteString("  ")
	}
}

func (e *encoder) value(v any, level int) {
	if r, ok := v.(*Ref); ok {
		e.enter(r)
		e.value(r.Subject(), level)
		delete(e.active, r)
		return
	}
	switch x := v.(type) {
	case nil:
		e.buf.WriteString("null")
	case bool:
		if x {
			e.buf.WriteString("true")
		} else {
			e.buf.WriteString("false")
		}
	case string:
		writeString(&e.buf, x)
	case Int:
		e.buf.WriteString(string(x))
	case float64:
		e.buf.WriteString(jsonFloat(x))
	case *Map:
		if x.Len() == 0 {
			e.buf.WriteString("{}")
			return
		}
		e.enter(x)
		e.buf.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				e.buf.WriteByte(',')
			}
			e.newline(level + 1)
			writeString(&e.buf, k)
			e.buf.WriteString(": ")
			e.value(x.vals[i], level+1)
		}
		e.newline(level)
		e.buf.WriteByte('}')
		delete(e.active, x)
	case *List:
		if len(x.Items) == 0 {
			e.buf.WriteString("[]")
			return
		}
		e.enter(x)
		e.buf.WriteByte('[')
		for i, item := range x.Items {
			if i > 0 {
				e.buf.WriteByte(',')
			}
			e.newline(level + 1)
			e.value(item, level+1)
		}
		e.newline(level)
		e.buf.WriteByte(']')
		delete(e.active, x)
	default:
		raise("TypeError", "Object of type %T is not JSON serializable", v)
	}
}

const hexDigits = "0123456789abcdef"

// writeString writes s as a JSON string the way Python's ensure_ascii
// encoder does: everything outside printable ASCII becomes \uXXXX, with
// surrogate pairs above U+FFFF.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r >= 0x20 && r <= 0x7e:
			buf.WriteByte(byte(r))
		case r > 0xffff:
			r1, r2 := utf16.EncodeRune(r)
			writeU(buf, r1)
			writeU(buf, r2)
		default:
			writeU(buf, r)
		}
	}
	buf.WriteByte('"')
}

func writeU(buf *bytes.Buffer, r rune) {
	buf.WriteString(`\u`)
	buf.WriteByte(hexDigits[r>>12&0xf])
	buf.WriteByte(hexDigits[r>>8&0xf])
	buf.WriteByte(hexDigits[r>>4&0xf])
	buf.WriteByte(hexDigits[r&0xf])
}

func jsonFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return pyFloatRepr(f)
}

// pyFloatRepr formats a finite float like Python's repr(): the shortest
// digits that round-trip, in fixed notation for exponents from -4 to 15
// (always with a decimal point) and scientific notation otherwise.
func pyFloatRepr(f float64) string {
	sign := ""
	if math.Signbit(f) {
		sign = "-"
		f = -f
	}
	// "d.ddde±XX" with the shortest round-tripping digits.
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(s, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)

	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es = "-"
			exp = -exp
		}
		e := strconv.Itoa(exp)
		if len(e) < 2 {
			e = "0" + e
		}
		return sign + m + "e" + es + e
	}

	point := exp + 1 // digits before the decimal point
	switch {
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		return sign + digits[:point] + "." + digits[point:]
	}
}
