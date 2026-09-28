package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	"github.com/mxpv/podsync/pkg/configschema"
)

// configFileHeader opens every file written by the admin interface.
var configFileHeader = []string{
	"Podsync configuration.",
	"",
	"Managed by the Podsync admin interface. You can still edit this file by hand: the admin",
	"interface detects hand edits, and it keeps previous versions next to this file as",
	"<name>.bak.<timestamp>. Comments added by hand are not preserved when the admin interface saves.",
}

// commentWidth is where generated comments wrap.
const commentWidth = 100

// parseConfigDocument parses a configuration file into a generic document of nested maps, the
// form the admin editor works on.
func parseConfigDocument(format configFormat, data []byte) (map[string]interface{}, error) {
	tree, err := parseConfigTree(format, data)
	if err != nil {
		return nil, err
	}
	return tree.ToMap(), nil
}

// renderConfig writes a document in the given format, ordered by the schema and commented with
// option descriptions where the format allows. The output is parsed back and must reproduce the
// document exactly; otherwise nothing is returned, so a writer bug can never reach disk.
func renderConfig(format configFormat, document map[string]interface{}, schema *configschema.Schema) ([]byte, error) {
	var (
		out []byte
		err error
	)
	switch format {
	case configFormatYAML:
		out, err = renderYAML(document, schema)
	case configFormatJSON:
		out, err = renderJSON(document, schema)
	default:
		out, err = renderTOML(document, schema)
	}
	if err != nil {
		return nil, err
	}

	parsed, err := parseConfigDocument(format, out)
	if err != nil {
		return nil, errors.Wrap(err, "internal error: the generated configuration does not parse")
	}
	want, err := normalizeConfigValue(document, "")
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(canonicalConfigValue(parsed), canonicalConfigValue(want)) {
		return nil, errors.New("internal error: the generated configuration does not match the edited values; nothing was written")
	}
	return out, nil
}

// canonicalConfigValue makes documents comparable across formats: empty lists and nil lists
// are equal, and all list values are []interface{}.
func canonicalConfigValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			out[key] = canonicalConfigValue(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			out = append(out, canonicalConfigValue(item))
		}
		return out
	case []string:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	case int:
		return int64(v)
	case time.Time:
		return v.UTC()
	default:
		return v
	}
}

// orderedKeys returns keys in schema declaration order, then any others alphabetically.
func orderedKeys(values map[string]interface{}, schema *configschema.Schema) []string {
	keys := make([]string, 0, len(values))
	seen := map[string]bool{}
	if schema != nil {
		for _, name := range schema.Order {
			for key := range values {
				if !seen[key] && strings.EqualFold(key, name) {
					keys = append(keys, key)
					seen[key] = true
				}
			}
		}
	}
	var rest []string
	for key := range values {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func childSchema(schema *configschema.Schema, key string) *configschema.Schema {
	return schema.Child(key)
}

func itemSchema(schema *configschema.Schema) *configschema.Schema {
	return schema.Item()
}

// describedKey reports whether a key's description should be written: struct options have one,
// map entries (feed IDs, token providers) do not.
func describedKey(parent *configschema.Schema, key string) string {
	if parent == nil || parent.Properties == nil {
		return ""
	}
	if property := childSchema(parent, key); property != nil {
		return property.Description
	}
	return ""
}

func wrapComment(text string, width int) []string {
	var (
		lines []string
		line  string
	)
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// ----- TOML -----

type tomlWriter struct {
	buf bytes.Buffer
}

func renderTOML(document map[string]interface{}, schema *configschema.Schema) ([]byte, error) {
	w := &tomlWriter{}
	for _, line := range configFileHeader {
		w.comment(line, "")
	}
	if err := w.table(nil, document, schema); err != nil {
		return nil, err
	}
	return w.buf.Bytes(), nil
}

func (w *tomlWriter) comment(text, indent string) {
	if text == "" {
		w.buf.WriteString(indent + "#\n")
		return
	}
	w.buf.WriteString(indent + "# " + text + "\n")
}

func (w *tomlWriter) describe(text, indent string) {
	for _, line := range wrapComment(text, commentWidth-len(indent)-2) {
		w.comment(line, indent)
	}
}

func isTableArray(value interface{}) bool {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 {
		return false
	}
	for _, item := range items {
		if _, ok := item.(map[string]interface{}); !ok {
			return false
		}
	}
	return true
}

// table writes the key/value pairs of a table, then its sub-tables and arrays of tables.
func (w *tomlWriter) table(path []string, values map[string]interface{}, schema *configschema.Schema) error {
	indent := strings.Repeat("  ", max(0, len(path)-1))
	keys := orderedKeys(values, schema)

	for _, key := range keys {
		value := values[key]
		if _, isMap := value.(map[string]interface{}); isMap || isTableArray(value) {
			continue
		}
		rendered, err := tomlValue(value)
		if err != nil {
			return errors.Wrapf(err, "%s", strings.Join(append(append([]string{}, path...), key), "."))
		}
		w.describe(describedKey(schema, key), indent)
		w.buf.WriteString(indent + tomlKey(key) + " = " + rendered + "\n")
	}

	for _, key := range keys {
		value := values[key]
		child := childSchema(schema, key)
		childPath := append(append([]string{}, path...), key)
		childIndent := strings.Repeat("  ", len(childPath)-1)
		switch v := value.(type) {
		case map[string]interface{}:
			w.buf.WriteString("\n")
			w.describe(describedKey(schema, key), childIndent)
			w.buf.WriteString(childIndent + "[" + tomlPath(childPath) + "]\n")
			if err := w.table(childPath, v, child); err != nil {
				return err
			}
		case []interface{}:
			if !isTableArray(v) {
				continue
			}
			for i, item := range v {
				w.buf.WriteString("\n")
				if i == 0 {
					w.describe(describedKey(schema, key), childIndent)
				}
				w.buf.WriteString(childIndent + "[[" + tomlPath(childPath) + "]]\n")
				if err := w.table(childPath, item.(map[string]interface{}), itemSchema(child)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var bareTOMLKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(key string) string {
	if bareTOMLKey.MatchString(key) {
		return key
	}
	return tomlString(key)
}

func tomlPath(path []string) string {
	parts := make([]string, len(path))
	for i, key := range path {
		parts[i] = tomlKey(key)
	}
	return strings.Join(parts, ".")
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlValue(value interface{}) (string, error) {
	switch v := value.(type) {
	case string:
		return tomlString(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case int:
		return strconv.Itoa(v), nil
	case float64:
		return formatConfigFloat(v)
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	case []string:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return tomlValue(items)
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			rendered, err := tomlValue(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, rendered)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]interface{}:
		// Only reached for tables nested inside inline arrays.
		keys := orderedKeys(v, nil)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			rendered, err := tomlValue(v[key])
			if err != nil {
				return "", err
			}
			parts = append(parts, tomlKey(key)+" = "+rendered)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", errors.Errorf("unsupported value %v (%T)", value, value)
	}
}

// formatConfigFloat writes a float that stays a float when parsed back (60 -> 60.0).
func formatConfigFloat(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.Errorf("unsupported number %v", f)
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s, nil
}

// ----- YAML -----

func renderYAML(document map[string]interface{}, schema *configschema.Schema) ([]byte, error) {
	root, err := yamlNode(document, schema)
	if err != nil {
		return nil, err
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	var header []string
	for _, line := range configFileHeader {
		header = append(header, strings.TrimSpace("# "+line))
	}
	doc.HeadComment = strings.Join(header, "\n")

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func yamlNode(value interface{}, schema *configschema.Schema) (*yaml.Node, error) {
	switch v := value.(type) {
	case map[string]interface{}:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, key := range orderedKeys(v, schema) {
			child, err := yamlNode(v[key], childSchema(schema, key))
			if err != nil {
				return nil, errors.Wrapf(err, "%s", key)
			}
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			if description := describedKey(schema, key); description != "" {
				keyNode.HeadComment = "# " + strings.Join(wrapComment(description, commentWidth-2), "\n# ")
			}
			node.Content = append(node.Content, keyNode, child)
		}
		return node, nil
	case []interface{}:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		flow := !isTableArray(v)
		if flow {
			node.Style = yaml.FlowStyle
		}
		for _, item := range v {
			child, err := yamlNode(item, itemSchema(schema))
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	case []string:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return yamlNode(items, schema)
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}, nil
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)}, nil
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}, nil
	case float64:
		s, err := formatConfigFloat(v)
		if err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: s}, nil
	case time.Time:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: v.Format(time.RFC3339Nano)}, nil
	default:
		return nil, errors.Errorf("unsupported value %v (%T)", value, value)
	}
}

// ----- JSON -----

func renderJSON(document map[string]interface{}, schema *configschema.Schema) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeJSONValue(&buf, document, schema, ""); err != nil {
		return nil, err
	}
	buf.WriteString("\n")
	return buf.Bytes(), nil
}

func writeJSONValue(buf *bytes.Buffer, value interface{}, schema *configschema.Schema, indent string) error {
	const step = "  "
	switch v := value.(type) {
	case map[string]interface{}:
		keys := orderedKeys(v, schema)
		if len(keys) == 0 {
			buf.WriteString("{}")
			return nil
		}
		buf.WriteString("{\n")
		for i, key := range keys {
			name, _ := json.Marshal(key)
			buf.WriteString(indent + step)
			buf.Write(name)
			buf.WriteString(": ")
			if err := writeJSONValue(buf, v[key], childSchema(schema, key), indent+step); err != nil {
				return errors.Wrapf(err, "%s", key)
			}
			if i < len(keys)-1 {
				buf.WriteString(",")
			}
			buf.WriteString("\n")
		}
		buf.WriteString(indent + "}")
		return nil
	case []interface{}:
		if len(v) == 0 {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteString("[\n")
		for i, item := range v {
			buf.WriteString(indent + step)
			if err := writeJSONValue(buf, item, itemSchema(schema), indent+step); err != nil {
				return err
			}
			if i < len(v)-1 {
				buf.WriteString(",")
			}
			buf.WriteString("\n")
		}
		buf.WriteString(indent + "]")
		return nil
	case []string:
		items := make([]interface{}, len(v))
		for i, item := range v {
			items[i] = item
		}
		return writeJSONValue(buf, items, schema, indent)
	case float64:
		s, err := formatConfigFloat(v)
		if err != nil {
			return err
		}
		buf.WriteString(s)
		return nil
	case string, bool, int64, int, time.Time:
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(encoded)
		return nil
	default:
		return errors.Errorf("unsupported value %v (%T)", value, value)
	}
}
