package admin

import (
	"strconv"
	"strings"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/configschema"
)

// SecretPlaceholder replaces secret values (API tokens, the password hash) in documents sent to
// the browser. When a document comes back, each placeholder is swapped for the value currently in
// the configuration file, so secrets are never exposed but stay unchanged unless replaced.
const SecretPlaceholder = "__podsync_secret_unchanged__"

// maskSecrets returns a copy of value with every secret replaced by SecretPlaceholder.
func maskSecrets(value interface{}, schema *configschema.Schema) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			child := schema.Child(key)
			if child != nil && child.Secret && !isMap(item) {
				out[key] = SecretPlaceholder
				continue
			}
			out[key] = maskSecrets(item, child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = maskSecrets(item, schema.Item())
		}
		return out
	default:
		return v
	}
}

// restoreSecrets replaces placeholders in a submitted document with the values at the same path in
// the current document. A placeholder with no current value is an error: the secret was never set,
// so there is nothing to keep.
func restoreSecrets(submitted, current interface{}, path []string) (interface{}, error) {
	switch v := submitted.(type) {
	case string:
		if v != SecretPlaceholder {
			return v, nil
		}
		if current == nil {
			return nil, errors.Errorf("%s: no existing secret to keep; enter a value or remove it", strings.Join(path, "."))
		}
		return current, nil
	case map[string]interface{}:
		currentMap, _ := current.(map[string]interface{})
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			restored, err := restoreSecrets(item, lookupKey(currentMap, key), append(path, key))
			if err != nil {
				return nil, err
			}
			out[key] = restored
		}
		return out, nil
	case []interface{}:
		currentList, _ := current.([]interface{})
		out := make([]interface{}, len(v))
		for i, item := range v {
			var currentItem interface{}
			if i < len(currentList) {
				currentItem = currentList[i]
			}
			restored, err := restoreSecrets(item, currentItem, append(path, "["+strconv.Itoa(i)+"]"))
			if err != nil {
				return nil, err
			}
			out[i] = restored
		}
		return out, nil
	default:
		return v, nil
	}
}

func lookupKey(values map[string]interface{}, key string) interface{} {
	if values == nil {
		return nil
	}
	if value, ok := values[key]; ok {
		return value
	}
	for name, value := range values {
		if strings.EqualFold(name, key) {
			return value
		}
	}
	return nil
}

func isMap(value interface{}) bool {
	_, ok := value.(map[string]interface{})
	return ok
}
