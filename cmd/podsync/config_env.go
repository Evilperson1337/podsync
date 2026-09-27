package main

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// configEnvPrefix marks environment variables that override configuration keys, with "__"
// separating path segments: PODSYNC__SERVER__PORT=9000 sets [server] port = 9000, and
// PODSYNC__STORAGE__LOCAL__DATA_DIR=/data sets [storage.local] data_dir. The double
// underscore keeps these apart from the older PODSYNC_* variables and allows keys that contain
// single underscores.
const configEnvPrefix = "PODSYNC__"

// applyConfigEnvOverrides sets configuration keys from PODSYNC__ environment variables on the
// parsed tree before it is decoded. Keys are matched case-insensitively; map keys (such as feed
// IDs) reuse the spelling already present in the file. Values are converted using the type of the
// target option. Values are never logged, since they may hold secrets.
func applyConfigEnvOverrides(tree *toml.Tree, target reflect.Type, environ []string) error {
	var names []string
	values := map[string]string{}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(strings.ToUpper(name), configEnvPrefix) {
			continue
		}
		names = append(names, name)
		values[name] = value
	}
	sort.Strings(names)

	for _, name := range names {
		segments := strings.Split(name[len(configEnvPrefix):], "__")
		keys, fieldType, err := resolveConfigEnvPath(tree, target, segments)
		if err != nil {
			return errors.Wrapf(err, "environment variable %s", name)
		}
		value, err := convertConfigEnvValue(fieldType, values[name])
		if err != nil {
			return errors.Wrapf(err, "environment variable %s", name)
		}
		tree.SetPath(keys, value)
		log.WithField("key", strings.Join(keys, ".")).Infof("configuration overridden by %s", name)
	}
	return nil
}

// resolveConfigEnvPath maps env path segments to tree keys and returns the target option's type.
func resolveConfigEnvPath(tree *toml.Tree, target reflect.Type, segments []string) ([]string, reflect.Type, error) {
	typ := target
	keys := make([]string, 0, len(segments))
	for i, segment := range segments {
		if segment == "" {
			return nil, nil, errors.New("empty path segment (check for a doubled \"__\")")
		}
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		switch typ.Kind() {
		case reflect.Struct:
			if typ == durationType || reflect.PointerTo(typ).Implements(tomlUnmarshalType) {
				return nil, nil, errors.Errorf("%q has no nested options", strings.Join(keys, "."))
			}
			field, ok := configFieldForKey(typ, segment)
			if !ok {
				return nil, nil, errors.Errorf("%q is not a configuration option", strings.ToLower(strings.Join(append(keys, segment), ".")))
			}
			name := strings.Split(field.Tag.Get("toml"), ",")[0]
			if name == "" {
				name = strings.ToLower(field.Name)
			}
			keys = append(keys, name)
			typ = field.Type
		case reflect.Map:
			keys = append(keys, existingConfigKey(tree, keys, segment))
			typ = typ.Elem()
		default:
			return nil, nil, errors.Errorf("%q cannot be set below %q; lists and values have no nested options",
				strings.ToLower(strings.Join(segments[i:], ".")), strings.Join(keys, "."))
		}
	}
	return keys, typ, nil
}

// existingConfigKey returns the spelling of segment already used in the tree at parent, or the
// lowercase segment when there is none.
func existingConfigKey(tree *toml.Tree, parent []string, segment string) string {
	var node interface{} = tree
	if len(parent) > 0 {
		node = tree.GetPath(parent)
	}
	if sub, ok := node.(*toml.Tree); ok {
		for _, key := range sub.Keys() {
			if strings.EqualFold(key, segment) {
				return key
			}
		}
	}
	return strings.ToLower(segment)
}

// convertConfigEnvValue converts an environment string to the value the decoder expects for typ.
func convertConfigEnvValue(typ reflect.Type, raw string) (interface{}, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch {
	case typ == durationType:
		return raw, nil
	case typ == reflect.TypeOf(StringSlice(nil)):
		// Space-separated values rotate like the PODSYNC_*_API_KEY variables.
		return strings.Fields(raw), nil
	}
	switch typ.Kind() {
	case reflect.String:
		return raw, nil
	case reflect.Bool:
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, errors.Errorf("expected true or false, got %q", raw)
		}
		return value, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, errors.Errorf("expected a whole number, got %q", raw)
		}
		return value, nil
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, errors.Errorf("expected a number, got %q", raw)
		}
		return value, nil
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.String {
			return splitConfigList(raw), nil
		}
	}
	return nil, errors.Errorf("options of type %s cannot be set from the environment; set them in the configuration file", typ)
}

// splitConfigList splits a comma-separated environment value into trimmed, non-empty items.
func splitConfigList(raw string) []string {
	var items []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
