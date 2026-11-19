package convert

import (
	"bytes"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// numberMode says how bare JSON numbers become values.
type numberMode int

const (
	// yamlNumbers follows PyYAML (YAML 1.1), which the Python tool uses to
	// read the spec: "1.5" is a float but "1e5" and "1.5e5" are strings,
	// because YAML 1.1 floats need a dot and a signed exponent.
	yamlNumbers numberMode = iota
	// jsonNumbers follows Python's json module, which jsonref uses to load
	// referenced files: anything with a fraction or exponent is a float.
	jsonNumbers
)

// ParseSpec parses an OpenAPI document. JSON goes through a fast JSON parser
// and anything else is parsed as YAML.
//
// By default values are resolved the way PyYAML does (YAML 1.1), as the
// Python tool reads specs with it. With modern set they follow the JSON spec
// for JSON and the YAML 1.2 core schema for YAML: 1e3 is a number, only
// true/false are booleans (not yes/no/on/off), 0755 is decimal 755 (0o755
// is octal) and 1:30 is a string.
func ParseSpec(data []byte, modern bool) (any, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		mode := yamlNumbers
		if modern {
			mode = jsonNumbers
		}
		v, err := parseJSON(data, mode)
		if err == nil {
			return v, nil
		}
		// Not strict JSON (comments, trailing commas, ...): YAML may cope.
		if v, yerr := parseYAML(data, modern); yerr == nil {
			return v, nil
		}
		return nil, err
	}
	return parseYAML(data, modern)
}

// ---------------------------------------------------------------------------
// JSON

type jsonParser struct {
	data []byte
	pos  int
	mode numberMode
}

func parseJSON(data []byte, mode numberMode) (v any, err error) {
	p := &jsonParser{data: data, mode: mode}
	defer func() {
		if r := recover(); r != nil {
			pe, ok := r.(jsonSyntaxError)
			if !ok {
				panic(r)
			}
			v, err = nil, pe
		}
	}()
	p.skipSpace()
	v = p.value()
	p.skipSpace()
	if p.pos != len(p.data) {
		p.fail("extra data")
	}
	return v, nil
}

type jsonSyntaxError struct {
	msg string
	pos int
}

func (e jsonSyntaxError) Error() string {
	return fmt.Sprintf("invalid JSON at byte %d: %s", e.pos, e.msg)
}

func (p *jsonParser) fail(msg string) { panic(jsonSyntaxError{msg, p.pos}) }

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) expect(b byte) {
	if p.pos >= len(p.data) || p.data[p.pos] != b {
		p.fail(fmt.Sprintf("expected %q", b))
	}
	p.pos++
}

func (p *jsonParser) literal(word string, v any) any {
	if !bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		p.fail("invalid literal")
	}
	p.pos += len(word)
	return v
}

func (p *jsonParser) value() any {
	if p.pos >= len(p.data) {
		p.fail("unexpected end of input")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == 'n':
		return p.literal("null", nil)
	case c == 'N' && p.mode == jsonNumbers:
		return p.literal("NaN", math.NaN())
	case c == 'I' && p.mode == jsonNumbers:
		return p.literal("Infinity", math.Inf(1))
	case c == '-' && p.mode == jsonNumbers && bytes.HasPrefix(p.data[p.pos:], []byte("-Infinity")):
		return p.literal("-Infinity", math.Inf(-1))
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	p.fail("unexpected character")
	return nil
}

func (p *jsonParser) object() *Map {
	p.pos++ // {
	m := NewMap()
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return m
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			p.fail("expected object key")
		}
		k := p.str()
		p.skipSpace()
		p.expect(':')
		p.skipSpace()
		m.Set(k, p.value())
		p.skipSpace()
		if p.pos < len(p.data) && p.data[p.pos] == ',' {
			p.pos++
			continue
		}
		p.expect('}')
		return m
	}
}

func (p *jsonParser) array() *List {
	p.pos++ // [
	l := &List{Items: []any{}}
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return l
	}
	for {
		p.skipSpace()
		l.Items = append(l.Items, p.value())
		p.skipSpace()
		if p.pos < len(p.data) && p.data[p.pos] == ',' {
			p.pos++
			continue
		}
		p.expect(']')
		return l
	}
}

func (p *jsonParser) str() string {
	p.pos++ // opening quote
	start := p.pos
	// Fast path: no escapes.
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == '"' {
			s := string(p.data[start:p.pos])
			p.pos++
			return s
		}
		if c == '\\' {
			break
		}
		if c < 0x20 {
			p.fail("control character in string")
		}
		p.pos++
	}
	var b strings.Builder
	b.Write(p.data[start:p.pos])
	for {
		if p.pos >= len(p.data) {
			p.fail("unterminated string")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String()
		case c < 0x20:
			p.fail("control character in string")
		case c != '\\':
			b.WriteByte(c)
			p.pos++
			continue
		}
		p.pos++ // backslash
		if p.pos >= len(p.data) {
			p.fail("unterminated string")
		}
		e := p.data[p.pos]
		p.pos++
		switch e {
		case '"', '\\', '/':
			b.WriteByte(e)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r := p.hex4()
			if utf16.IsSurrogate(r) && bytes.HasPrefix(p.data[p.pos:], []byte(`\u`)) {
				save := p.pos
				p.pos += 2
				r2 := p.hex4()
				if combined := utf16.DecodeRune(r, r2); combined != utf8.RuneError {
					r = combined
				} else {
					p.pos = save
				}
			}
			b.WriteRune(r)
		default:
			p.fail("invalid escape")
		}
	}
}

func (p *jsonParser) hex4() rune {
	if p.pos+4 > len(p.data) {
		p.fail("short \\u escape")
	}
	n, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		p.fail("invalid \\u escape")
	}
	p.pos += 4
	return rune(n)
}

func (p *jsonParser) number() any {
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
	}
	digits := func() int {
		n := 0
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
			n++
		}
		return n
	}
	if p.pos < len(p.data) && p.data[p.pos] == '0' {
		p.pos++
	} else if digits() == 0 {
		p.fail("invalid number")
	}
	hasFrac, hasExp, signedExp := false, false, false
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++
		hasFrac = true
		if digits() == 0 {
			p.fail("invalid number")
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		hasExp = true
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
			signedExp = true
		}
		if digits() == 0 {
			p.fail("invalid number")
		}
	}
	text := string(p.data[start:p.pos])

	if !hasFrac && !hasExp {
		if strings.TrimLeft(text, "-0") == "" {
			return Int("0")
		}
		return Int(text)
	}
	if p.mode == yamlNumbers && (!hasFrac || (hasExp && !signedExp)) {
		return text
	}
	f, _ := strconv.ParseFloat(text, 64) // overflow gives ±Inf, as Python does
	return f
}

// ---------------------------------------------------------------------------
// YAML

func parseYAML(data []byte, yaml12 bool) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		return nil, nil
	}
	b := &yamlBuilder{seen: map[*yaml.Node]any{}, yaml12: yaml12}
	var out any
	if err := catch(func() { out = b.node(&doc) }); err != nil {
		return nil, err
	}
	return out, nil
}

type yamlBuilder struct {
	seen   map[*yaml.Node]any // aliases share one object, as in PyYAML
	yaml12 bool               // YAML 1.2 core schema instead of PyYAML's 1.1 rules
}

func (b *yamlBuilder) node(n *yaml.Node) any {
	if v, ok := b.seen[n]; ok {
		return v
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return b.node(n.Content[0])
	case yaml.AliasNode:
		return b.node(n.Alias)
	case yaml.SequenceNode:
		l := &List{Items: make([]any, 0, len(n.Content))}
		b.seen[n] = l
		for _, c := range n.Content {
			l.Items = append(l.Items, b.node(c))
		}
		return l
	case yaml.MappingNode:
		m := NewMap()
		b.seen[n] = m
		pairs := flattenMapping(n)
		for i := 0; i+1 < len(pairs); i += 2 {
			m.Set(jsonKey(b.node(pairs[i])), b.node(pairs[i+1]))
		}
		return m
	case yaml.ScalarNode:
		if b.yaml12 {
			return scalar12(n)
		}
		return scalar(n)
	}
	return nil
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isMergeKey(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!merge"
}

// flattenMapping applies "<<" merge keys the way PyYAML does: merged pairs
// come first, then the mapping's own pairs, so later keys override values
// while keeping the earlier position.
func flattenMapping(n *yaml.Node) []*yaml.Node {
	var merged, own []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !isMergeKey(k) {
			own = append(own, k, v)
			continue
		}
		v = resolveAlias(v)
		switch v.Kind {
		case yaml.MappingNode:
			merged = append(merged, flattenMapping(v)...)
		case yaml.SequenceNode:
			var sub [][]*yaml.Node
			for _, s := range v.Content {
				s = resolveAlias(s)
				if s.Kind != yaml.MappingNode {
					raise("ConstructorError", "expected a mapping for merging, but found %v", s.Tag)
				}
				sub = append(sub, flattenMapping(s))
			}
			for i := len(sub) - 1; i >= 0; i-- {
				merged = append(merged, sub[i]...)
			}
		default:
			raise("ConstructorError", "expected a mapping or list of mappings for merging, but found %v", v.Tag)
		}
	}
	return append(merged, own...)
}

// jsonKey turns a mapping key into the string json.dumps would write.
func jsonKey(k any) string {
	switch x := k.(type) {
	case string:
		return x
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case Int:
		return string(x)
	case float64:
		return jsonFloat(x)
	}
	raise("ConstructorError", "found unhashable key")
	return ""
}

var (
	yamlBool  = regexp.MustCompile(`^(?:yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)$`)
	yamlFloat = regexp.MustCompile(`^(?:[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?|\.[0-9_]+(?:[eE][-+][0-9]+)?|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	yamlInt   = regexp.MustCompile(`^(?:[-+]?0b[0-1_]+|[-+]?0[0-7_]+|[-+]?(?:0|[1-9][0-9_]*)|[-+]?0x[0-9a-fA-F_]+|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+)$`)
	yamlNull  = regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)
)

// scalar resolves a YAML scalar with PyYAML's YAML 1.1 rules. Quoted and
// block scalars are strings unless explicitly tagged. Timestamps stay
// strings (PyYAML would make dates that json.dumps can't write).
func scalar(n *yaml.Node) any {
	tag := ""
	if n.Style&yaml.TaggedStyle != 0 {
		tag = n.ShortTag()
	} else if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return n.Value
	}
	v := n.Value
	switch {
	case tag == "!!str":
		return v
	case tag == "!!null" || (tag == "" && yamlNull.MatchString(v)):
		return nil
	case tag == "!!bool" || (tag == "" && yamlBool.MatchString(v)):
		switch strings.ToLower(v) {
		case "yes", "true", "on":
			return true
		}
		return false
	case tag == "!!float" || (tag == "" && yamlFloat.MatchString(v)):
		return yamlFloatValue(v)
	case tag == "!!int" || (tag == "" && yamlInt.MatchString(v)):
		return yamlIntValue(v)
	}
	return v
}

func splitSign(v string) (string, string) {
	v = strings.ReplaceAll(v, "_", "")
	sign := ""
	if v != "" && (v[0] == '-' || v[0] == '+') {
		if v[0] == '-' {
			sign = "-"
		}
		v = v[1:]
	}
	return sign, v
}

func yamlIntValue(s string) any {
	sign, v := splitSign(s)
	n := new(big.Int)
	ok := true
	switch {
	case v == "0":
	case strings.HasPrefix(v, "0b"):
		_, ok = n.SetString(v[2:], 2)
	case strings.HasPrefix(v, "0x"):
		_, ok = n.SetString(v[2:], 16)
	case strings.HasPrefix(v, "0"):
		_, ok = n.SetString(v[1:], 8)
	case strings.Contains(v, ":"):
		base := big.NewInt(1)
		parts := strings.Split(v, ":")
		for i := len(parts) - 1; i >= 0; i-- {
			d, _ := strconv.ParseInt(parts[i], 10, 64)
			n.Add(n, new(big.Int).Mul(big.NewInt(d), base))
			base.Mul(base, big.NewInt(60))
		}
	default:
		_, ok = n.SetString(v, 10)
	}
	if !ok {
		raise("ValueError", "invalid literal for int(): %s", pyRepr(s))
	}
	if sign == "-" {
		n.Neg(n)
	}
	return Int(n.String())
}

func yamlFloatValue(s string) any {
	sign, v := splitSign(strings.ToLower(s))
	var f float64
	switch {
	case v == ".inf":
		f = math.Inf(1)
	case v == ".nan":
		return math.NaN()
	case strings.Contains(v, ":"):
		base := 1.0
		parts := strings.Split(v, ":")
		for i := len(parts) - 1; i >= 0; i-- {
			d, _ := strconv.ParseFloat(parts[i], 64)
			f += d * base
			base *= 60
		}
	default:
		var err error
		f, err = strconv.ParseFloat(v, 64)
		if err != nil && !strings.Contains(err.Error(), "out of range") {
			raise("ValueError", "could not convert string to float: %s", pyRepr(s))
		}
	}
	if sign == "-" {
		f = -f
	}
	return f
}

// YAML 1.2 core schema (https://yaml.org/spec/1.2.2/#1032-tag-resolution).
var (
	core12Null  = regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)
	core12Bool  = regexp.MustCompile(`^(?:true|True|TRUE|false|False|FALSE)$`)
	core12Int   = regexp.MustCompile(`^(?:[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$`)
	core12Float = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$`)
	core12Inf   = regexp.MustCompile(`^[-+]?\.(?:inf|Inf|INF)$`)
	core12NaN   = regexp.MustCompile(`^\.(?:nan|NaN|NAN)$`)
)

// scalar12 resolves a YAML scalar with the YAML 1.2 core schema.
func scalar12(n *yaml.Node) any {
	tag := ""
	if n.Style&yaml.TaggedStyle != 0 {
		tag = n.ShortTag()
	} else if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return n.Value
	}
	v := n.Value
	switch {
	case tag == "!!str":
		return v
	case tag == "!!null" || (tag == "" && core12Null.MatchString(v)):
		return nil
	case tag == "!!bool" || (tag == "" && core12Bool.MatchString(v)):
		return strings.ToLower(v) == "true"
	case tag == "!!int" || (tag == "" && core12Int.MatchString(v)):
		n := new(big.Int)
		var ok bool
		switch {
		case strings.HasPrefix(v, "0o"):
			_, ok = n.SetString(v[2:], 8)
		case strings.HasPrefix(v, "0x"):
			_, ok = n.SetString(v[2:], 16)
		default:
			_, ok = n.SetString(strings.TrimPrefix(v, "+"), 10)
		}
		if !ok {
			raise("ValueError", "invalid integer %s", pyRepr(v))
		}
		return Int(n.String())
	case tag == "" && core12Inf.MatchString(v):
		if strings.HasPrefix(v, "-") {
			return math.Inf(-1)
		}
		return math.Inf(1)
	case tag == "" && core12NaN.MatchString(v):
		return math.NaN()
	case tag == "!!float" || (tag == "" && core12Float.MatchString(v)):
		f, err := strconv.ParseFloat(v, 64)
		if err != nil && !strings.Contains(err.Error(), "out of range") {
			raise("ValueError", "could not convert string to float: %s", pyRepr(v))
		}
		return f
	}
	return v
}
