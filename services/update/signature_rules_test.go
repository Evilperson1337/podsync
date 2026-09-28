package update

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mxpv/podsync/pkg/feed"
)

// TestReadSignatureRules verifies rules.json parsing.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Ensures rules are loaded when file exists.
func TestReadSignatureRules(t *testing.T) {
	file, err := os.CreateTemp("", "rules-*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(`{"rules":[{"file":"intro.wav","action":"cut_before","pre":0,"post":0}]}`); err != nil {
		_ = file.Close()
		t.Fatalf("write json: %v", err)
	}
	_ = file.Close()

	rules, ok, err := ReadSignatureRules(file.Name())
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok true")
	}
	if len(rules.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules.Rules))
	}
}

func TestResolveSignaturesRoot(t *testing.T) {
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	if got := ResolveSignaturesRoot("", "/data"); got != "/data" {
		t.Fatalf("expected local data dir fallback, got %q", got)
	}
	if got := ResolveSignaturesRoot("", ""); got != "" {
		t.Fatalf("expected empty root without local storage, got %q", got)
	}

	t.Setenv("PODSYNC_SIGNATURES_DIR", "/env")
	if got := ResolveSignaturesRoot("", "/data"); got != "/env" {
		t.Fatalf("expected env override, got %q", got)
	}
	if got := ResolveSignaturesRoot(" /configured ", "/data"); got != "/configured" {
		t.Fatalf("expected configured root to win, got %q", got)
	}
}

func TestSignatureFilePath(t *testing.T) {
	if got := SignatureFilePath("/data", "show", "intro.wav"); got != filepath.Join("/data", "show", "signatures", "intro.wav") {
		t.Fatalf("unexpected relative resolution: %q", got)
	}
	if got := SignatureFilePath("", "show", "/sigs/intro.wav"); got != "/sigs/intro.wav" {
		t.Fatalf("absolute file should be used as-is, got %q", got)
	}
	if got := SignatureFilePath("", "show", "intro.wav"); got != "" {
		t.Fatalf("relative file without root should not resolve, got %q", got)
	}
}

func TestLoadFeedSignatureRulesPrecedence(t *testing.T) {
	root := t.TempDir()
	sigDir := SignaturesDir(root, "show")
	if err := os.MkdirAll(sigDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sigDir, "rules.json"), []byte(`{"rules":[{"file":"json.wav","action":"cut_after"}]}`), 0644); err != nil {
		t.Fatal(err)
	}

	withTOML := &feed.Config{ID: "show", SignatureRules: []feed.SignatureRule{{File: "toml.wav", Action: "cut_before"}}}
	rules, source, ignored, err := LoadFeedSignatureRules(root, withTOML)
	if err != nil || source != SignatureRulesSourceConfig || !ignored || len(rules) != 1 || rules[0].File != "toml.wav" {
		t.Fatalf("TOML rules should win: rules=%v source=%q ignored=%v err=%v", rules, source, ignored, err)
	}

	rules, source, ignored, err = LoadFeedSignatureRules(root, &feed.Config{ID: "show"})
	if err != nil || source != SignatureRulesSourceRulesJSON || ignored || len(rules) != 1 || rules[0].File != "json.wav" {
		t.Fatalf("rules.json should be the fallback: rules=%v source=%q ignored=%v err=%v", rules, source, ignored, err)
	}

	rules, source, _, err = LoadFeedSignatureRules(root, &feed.Config{ID: "other"})
	if err != nil || source != "" || len(rules) != 0 {
		t.Fatalf("no rules expected: rules=%v source=%q err=%v", rules, source, err)
	}

	rules, source, _, err = LoadFeedSignatureRules("", withTOML)
	if err != nil || source != SignatureRulesSourceConfig || len(rules) != 1 {
		t.Fatalf("TOML rules should load without a root: rules=%v source=%q err=%v", rules, source, err)
	}
}
