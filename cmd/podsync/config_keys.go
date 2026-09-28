package main

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml"
)

// unknownConfigKey is a key in the configuration file that no configuration field reads.
type unknownConfigKey struct {
	Path string
	// Segments is Path as keys, with list indexes as decimal strings.
	Segments []string
	Line     int
}

func (k unknownConfigKey) String() string {
	if k.Line > 0 {
		return fmt.Sprintf("%s (line %d)", k.Path, k.Line)
	}
	return k.Path
}

var (
	durationType      = reflect.TypeOf(time.Duration(0))
	tomlUnmarshalType = reflect.TypeOf((*toml.Unmarshaler)(nil)).Elem()
)

// findUnknownConfigKeys walks the parsed TOML tree alongside the configuration struct type and
// returns keys that do not correspond to any field. The TOML decoder silently ignores such keys,
// so a typo like "signature_rule" would otherwise disable a feature without any error.
//
// Field matching mirrors the decoder: the toml tag name, or the field name, compared
// case-insensitively.
func findUnknownConfigKeys(tree *toml.Tree, target reflect.Type) []unknownConfigKey {
	var found []unknownConfigKey
	walkConfigTree(tree, target, "", nil, 0, false, &found)
	sort.Slice(found, func(i, j int) bool {
		if found[i].Line != found[j].Line {
			return found[i].Line < found[j].Line
		}
		return found[i].Path < found[j].Path
	})
	return found
}

// parentLine is the line of the enclosing key. The TOML library reports positions inside inline
// tables relative to the table, so a key that appears before its parent is inside an inline table;
// line numbers are then omitted for it and everything below it rather than reported wrong.
func walkConfigTree(value interface{}, typ reflect.Type, path string, segments []string, parentLine int, inline bool, found *[]unknownConfigKey) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	// Types that decode themselves (e.g. StringSlice) and scalar types have no nested keys to check.
	if reflect.PointerTo(typ).Implements(tomlUnmarshalType) || typ == durationType {
		return
	}

	switch typ.Kind() {
	case reflect.Struct:
		tree, ok := value.(*toml.Tree)
		if !ok {
			return
		}
		for _, key := range tree.Keys() {
			keyPath := joinConfigPath(path, key)
			keySegments := append(append([]string{}, segments...), key)
			line := tree.GetPositionPath([]string{key}).Line
			keyInline := inline || line < parentLine
			reported := line
			if keyInline {
				reported = 0
			}
			field, ok := configFieldForKey(typ, key)
			if !ok {
				*found = append(*found, unknownConfigKey{Path: keyPath, Segments: keySegments, Line: reported})
				continue
			}
			walkConfigTree(tree.GetPath([]string{key}), field.Type, keyPath, keySegments, line, keyInline, found)
		}

	case reflect.Map:
		tree, ok := value.(*toml.Tree)
		if !ok {
			return
		}
		for _, key := range tree.Keys() {
			line := tree.GetPositionPath([]string{key}).Line
			walkConfigTree(tree.GetPath([]string{key}), typ.Elem(), joinConfigPath(path, key), append(append([]string{}, segments...), key), line, inline || line < parentLine, found)
		}

	case reflect.Slice, reflect.Array:
		switch items := value.(type) {
		case []*toml.Tree:
			for i, item := range items {
				walkConfigTree(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i), append(append([]string{}, segments...), strconv.Itoa(i)), parentLine, inline, found)
			}
		case []interface{}:
			for i, item := range items {
				walkConfigTree(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i), append(append([]string{}, segments...), strconv.Itoa(i)), parentLine, inline, found)
			}
		}
	}
}

// configFieldForKey finds the struct field the decoder would fill for key.
func configFieldForKey(typ reflect.Type, key string) (reflect.StructField, bool) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("toml"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if strings.EqualFold(name, key) || strings.EqualFold(field.Name, key) {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

func joinConfigPath(path, key string) string {
	if strings.ContainsAny(key, ". ") {
		key = fmt.Sprintf("%q", key)
	}
	if path == "" {
		return key
	}
	return path + "." + key
}
