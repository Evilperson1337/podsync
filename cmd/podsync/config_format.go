package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// configFormat is the syntax of a configuration file, chosen by its extension.
type configFormat string

const (
	configFormatTOML configFormat = "toml"
	configFormatYAML configFormat = "yaml"
	configFormatJSON configFormat = "json"
)

// configFormatFor picks the format from the file extension. Anything that is not YAML or JSON
// is read as TOML, the original and default format.
func configFormatFor(path string) configFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return configFormatYAML
	case ".json":
		return configFormatJSON
	default:
		return configFormatTOML
	}
}

// parseConfigTree parses a configuration file into a TOML tree. YAML and JSON documents are
// converted to the same tree, so every format shares decoding, custom value types, duration
// strings and unknown-key checks.
func parseConfigTree(format configFormat, data []byte) (*toml.Tree, error) {
	if format == configFormatTOML {
		return toml.LoadBytes(data)
	}

	var raw interface{}
	switch format {
	case configFormatYAML:
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
	case configFormatJSON:
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
	}
	if raw == nil {
		return toml.TreeFromMap(map[string]interface{}{})
	}
	normalized, err := normalizeConfigValue(raw, "")
	if err != nil {
		return nil, err
	}
	document, ok := normalized.(map[string]interface{})
	if !ok {
		return nil, errors.Errorf("the top level of the configuration must be a mapping, got %T", raw)
	}
	return toml.TreeFromMap(document)
}

// normalizeConfigValue converts YAML/JSON values into types the TOML tree and decoder accept:
// string-keyed maps, int64/float64 numbers, and no null values (a null means "not set").
func normalizeConfigValue(value interface{}, path string) (interface{}, error) {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			if item == nil {
				continue
			}
			normalized, err := normalizeConfigValue(item, joinConfigPath(path, key))
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			out[fmt.Sprint(key)] = item
		}
		return normalizeConfigValue(out, path)
	case []interface{}:
		out := make([]interface{}, 0, len(v))
		for i, item := range v {
			if item == nil {
				return nil, errors.Errorf("%s[%d]: null values are not allowed in lists", path, i)
			}
			normalized, err := normalizeConfigValue(item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, normalized)
		}
		return out, nil
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i, nil
		}
		f, err := v.Float64()
		if err != nil {
			return nil, errors.Wrapf(err, "%s: invalid number %q", path, v.String())
		}
		return f, nil
	case int:
		return int64(v), nil
	default:
		return v, nil
	}
}
