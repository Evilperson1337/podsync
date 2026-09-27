package main

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml"
)

// unknownConfigKey is a key in the configuration file that no configuration field reads.
type unknownConfigKey struct {
	Path string
	Line int
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
	walkConfigTree(tree, target, "", &found)
	sort.Slice(found, func(i, j int) bool {
		if found[i].Line != found[j].Line {
			return found[i].Line < found[j].Line
		}
		return found[i].Path < found[j].Path
	})
	return found
}

func walkConfigTree(value interface{}, typ reflect.Type, path string, found *[]unknownConfigKey) {
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
			field, ok := configFieldForKey(typ, key)
			if !ok {
				*found = append(*found, unknownConfigKey{Path: keyPath, Line: tree.GetPositionPath([]string{key}).Line})
				continue
			}
			walkConfigTree(tree.GetPath([]string{key}), field.Type, keyPath, found)
		}

	case reflect.Map:
		tree, ok := value.(*toml.Tree)
		if !ok {
			return
		}
		for _, key := range tree.Keys() {
			walkConfigTree(tree.GetPath([]string{key}), typ.Elem(), joinConfigPath(path, key), found)
		}

	case reflect.Slice, reflect.Array:
		switch items := value.(type) {
		case []*toml.Tree:
			for i, item := range items {
				walkConfigTree(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i), found)
			}
		case []interface{}:
			for i, item := range items {
				walkConfigTree(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i), found)
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
