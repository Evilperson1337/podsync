package configschema

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type flexible []string

func (flexible) ConfigSchema() Schema {
	return Schema{Type: []string{"string", "array"}, Items: &Schema{Type: "string"}}
}

type sample struct {
	Name     string              `toml:"name" doc:"Display name."`
	Mode     string              `toml:"mode" enum:"a,b"`
	Count    int                 `toml:"count"`
	Ratio    float64             `toml:"ratio"`
	On       bool                `toml:"on"`
	Every    time.Duration       `toml:"every"`
	Tags     []string            `toml:"tags"`
	Nested   *sampleNested       `toml:"nested"`
	Items    []sampleNested      `toml:"items"`
	ByName   map[string]flexible `toml:"by_name" secret:"true"`
	Implicit string
	Skipped  string `toml:"-"`
	hidden   string //nolint:unused
}

type sampleNested struct {
	Value string `toml:"value"`
}

func TestGenerate(t *testing.T) {
	schema := Generate(reflect.TypeOf(sample{}))

	assert.Equal(t, "object", schema.Type)
	assert.Equal(t, []string{"name", "mode", "count", "ratio", "on", "every", "tags", "nested", "items", "by_name", "implicit"}, schema.Order)
	assert.NotContains(t, schema.Properties, "-")

	p := schema.Properties
	assert.Equal(t, "Display name.", p["name"].Description)
	assert.Equal(t, []string{"a", "b"}, p["mode"].Enum)
	assert.Equal(t, "integer", p["count"].Type)
	assert.Equal(t, "number", p["ratio"].Type)
	assert.Equal(t, "boolean", p["on"].Type)
	assert.Equal(t, "duration", p["every"].Format)
	assert.Equal(t, "string", p["tags"].Items.Type)
	assert.Equal(t, "string", p["nested"].Properties["value"].Type)
	assert.Equal(t, "object", p["items"].Items.Type)
	assert.Equal(t, "string", p["implicit"].Type)

	byName := p["by_name"]
	assert.True(t, byName.Secret)
	assert.True(t, byName.AdditionalProperties.Secret, "secrecy applies to nested values")
	assert.Equal(t, []string{"string", "array"}, byName.AdditionalProperties.Type, "custom types describe themselves")
	assert.False(t, p["name"].Secret)

	_, err := json.Marshal(schema)
	require.NoError(t, err)
}
