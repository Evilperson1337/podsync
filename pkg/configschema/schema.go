// Package configschema generates a JSON Schema description of the Podsync configuration from the
// configuration structs, so the admin interface can render and validate settings without a
// hand-written copy of every option.
//
// Struct tags drive the output:
//
//	toml:"name"      the configuration key (the same tag the decoder uses)
//	doc:"..."        a human-readable description
//	enum:"a,b,c"     the allowed values
//	secret:"true"    a write-only value, such as API tokens, never sent back to the browser
package configschema

import (
	"reflect"
	"strings"
	"time"
)

// Schema is the subset of JSON Schema used to describe configuration.
type Schema struct {
	Type                 interface{}        `json:"type,omitempty"`
	Description          string             `json:"description,omitempty"`
	Format               string             `json:"format,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
	// Order lists property names in declaration order (JSON objects are unordered).
	Order []string `json:"x-order,omitempty"`
	// Secret marks write-only values.
	Secret bool `json:"x-secret,omitempty"`
}

// Provider lets a type with custom decoding describe its own schema.
type Provider interface {
	ConfigSchema() Schema
}

var (
	durationType = reflect.TypeOf(time.Duration(0))
	providerType = reflect.TypeOf((*Provider)(nil)).Elem()
)

// Generate returns the schema for a configuration type.
func Generate(typ reflect.Type) *Schema {
	return generate(typ)
}

func generate(typ reflect.Type) *Schema {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Implements(providerType) || reflect.PointerTo(typ).Implements(providerType) {
		schema := reflect.New(typ).Interface().(Provider).ConfigSchema()
		return &schema
	}
	if typ == durationType {
		return &Schema{Type: "string", Format: "duration", Description: `A duration such as "30m", "6h" or "2h45m".`}
	}

	switch typ.Kind() {
	case reflect.Struct:
		schema := &Schema{Type: "object", Properties: map[string]*Schema{}}
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
				name = strings.ToLower(field.Name)
			}
			property := generate(field.Type)
			if doc := field.Tag.Get("doc"); doc != "" {
				property.Description = doc
			}
			if enum := field.Tag.Get("enum"); enum != "" {
				property.Enum = strings.Split(enum, ",")
			}
			if field.Tag.Get("secret") == "true" {
				markSecret(property)
			}
			schema.Properties[name] = property
			schema.Order = append(schema.Order, name)
		}
		return schema
	case reflect.Map:
		return &Schema{Type: "object", AdditionalProperties: generate(typ.Elem())}
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: generate(typ.Elem())}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	default:
		return &Schema{}
	}
}

// markSecret flags a value and everything inside it as write-only.
func markSecret(schema *Schema) {
	if schema == nil {
		return
	}
	schema.Secret = true
	markSecret(schema.Items)
	markSecret(schema.AdditionalProperties)
	for _, property := range schema.Properties {
		markSecret(property)
	}
}
