package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/mxpv/podsync/pkg/configschema"
	"github.com/mxpv/podsync/pkg/feed"
)

func TestLoadConfig_Admin(t *testing.T) {
	t.Run("disabled by default with defaults applied", func(t *testing.T) {
		cfg, err := LoadConfig(writeConfigFile(t, "config.toml", "[server]\nport = 8080\n"))
		require.NoError(t, err)
		assert.False(t, cfg.Admin.Enabled)
		assert.Equal(t, 8081, cfg.Admin.Port)
		assert.Equal(t, "Remote-User", cfg.Admin.UserHeader)
	})

	t.Run("valid proxy mode", func(t *testing.T) {
		cfg, err := LoadConfig(writeConfigFile(t, "config.toml", `
[admin]
enabled = true
auth = "proxy"
trusted_proxies = ["172.18.0.0/16"]
`))
		require.NoError(t, err)
		assert.True(t, cfg.Admin.Enabled)
	})

	failures := map[string]struct{ body, want string }{
		"proxy without trusted proxies": {"[admin]\nenabled = true\nauth = \"proxy\"\n", "admin.trusted_proxies is required"},
		"missing auth":                  {"[admin]\nenabled = true\n", "admin.auth is required"},
		"password without hash":         {"[admin]\nenabled = true\nauth = \"password\"\n", "podsync --hash-password"},
		"same port as server": {
			"[server]\nport = 9000\n[admin]\nenabled = true\nport = 9000\nauth = \"proxy\"\ntrusted_proxies = [\"10.0.0.1\"]\n",
			"must differ from server.port",
		},
		"same port as default server port": {
			"[admin]\nenabled = true\nport = 8080\nauth = \"proxy\"\ntrusted_proxies = [\"10.0.0.1\"]\n",
			"must differ from server.port",
		},
	}
	for name, tt := range failures {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeConfigFile(t, "config.toml", tt.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestConfigSchema(t *testing.T) {
	schema := configschema.Generate(reflect.TypeOf(Config{}))

	require.Contains(t, schema.Properties, "admin")
	assert.True(t, schema.Properties["tokens"].Secret, "API tokens are write-only")
	assert.True(t, schema.Properties["tokens"].AdditionalProperties.Secret)
	assert.True(t, schema.Properties["admin"].Properties["password_hash"].Secret)
	assert.False(t, schema.Properties["admin"].Properties["port"].Secret)
	assert.Equal(t, []string{"proxy", "password"}, schema.Properties["admin"].Properties["auth"].Enum)

	feedSchema := schema.Properties["feeds"].AdditionalProperties
	require.NotNil(t, feedSchema)
	assert.Equal(t, []string{"audio", "video", "custom"}, feedSchema.Properties["format"].Enum)
	assert.Equal(t, "duration", feedSchema.Properties["update_period"].Format)
	assert.Equal(t, "number", feedSchema.Properties["signature_rules"].Items.Properties["post"].Type)
	assert.Equal(t, []string{"string", "array"}, schema.Properties["tokens"].AdditionalProperties.Type)
}

// TestConfigSchemaCoversExample checks that every key used in config.toml.example appears in the
// generated schema, so the admin interface can describe every documented option.
func TestConfigSchemaCoversExample(t *testing.T) {
	data, err := os.ReadFile("../../config.toml.example")
	require.NoError(t, err)
	tree, err := toml.LoadBytes(data)
	require.NoError(t, err)

	schema := configschema.Generate(reflect.TypeOf(Config{}))
	var missing []string
	var walk func(node interface{}, s *configschema.Schema, path string)
	walk = func(node interface{}, s *configschema.Schema, path string) {
		switch value := node.(type) {
		case *toml.Tree:
			for _, key := range value.Keys() {
				child := value.GetPath([]string{key})
				var childSchema *configschema.Schema
				switch {
				case s.Properties != nil:
					for name, candidate := range s.Properties {
						if strings.EqualFold(name, key) {
							childSchema = candidate
						}
					}
				case s.AdditionalProperties != nil:
					childSchema = s.AdditionalProperties
				}
				if childSchema == nil {
					missing = append(missing, path+key)
					continue
				}
				walk(child, childSchema, path+key+".")
			}
		case []*toml.Tree:
			for _, item := range value {
				if s.Items != nil {
					walk(item, s.Items, path)
				}
			}
		}
	}
	walk(tree, schema, "")
	assert.Empty(t, missing, "keys in config.toml.example that the schema does not describe")
}

func TestHashAdminPassword(t *testing.T) {
	_, err := hashAdminPassword("short")
	require.Error(t, err)

	hash, err := hashAdminPassword("a long enough password")
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("a long enough password")))
}

func TestRunHashPasswordFromStdin(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString("a long enough password\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	var stdout, stderr strings.Builder
	require.NoError(t, runHashPassword(reader, &stdout, &stderr))
	hash := strings.TrimSpace(stdout.String())
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("a long enough password")), "trailing newline is not part of the password")
	assert.Contains(t, stderr.String(), "password_hash")
}

func TestAdminRuntimeFeeds(t *testing.T) {
	reloader, _, _, _ := newTestReloader(t, reloadBaseConfig)
	runtime := adminRuntime{reloader: reloader, schedule: reloader.schedule}

	feeds := runtime.Feeds()
	require.Len(t, feeds, 1)
	assert.Equal(t, "show", feeds[0].Config.ID)
	assert.Equal(t, feedCronSchedule(feeds[0].Config), feeds[0].Schedule)
	assert.False(t, feeds[0].NextRun.IsZero(), "the next run comes from the cron entry")
	assert.True(t, feeds[0].NextRun.After(time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)))

	// A feed missing from the schedule still reports its configured schedule.
	reloader.current.Feeds["unscheduled"] = &feed.Config{ID: "unscheduled", UpdatePeriod: time.Hour}
	assert.Len(t, runtime.Feeds(), 2)
}

func TestRestartOnlyChangesIncludesAdmin(t *testing.T) {
	after := &Config{}
	after.Admin.Enabled = true
	assert.Equal(t, []string{"admin"}, restartOnlyChanges(&Config{}, after))
}

// TestConfigSchemaDescribesEveryOption keeps the admin editor's help text and generated file
// comments complete: every option needs a doc tag.
func TestConfigSchemaDescribesEveryOption(t *testing.T) {
	schema := configschema.Generate(reflect.TypeOf(Config{}))
	var missing []string
	var walk func(s *configschema.Schema, path string)
	walk = func(s *configschema.Schema, path string) {
		if s == nil {
			return
		}
		for name, property := range s.Properties {
			if strings.TrimSpace(property.Description) == "" {
				missing = append(missing, path+name)
			}
			walk(property, path+name+".")
		}
		walk(s.Items, path+"[].")
		walk(s.AdditionalProperties, path+"<id>.")
	}
	walk(schema, "")
	assert.Empty(t, missing, "options without a doc tag")
}
