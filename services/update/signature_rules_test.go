package update

import (
	"os"
	"testing"
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
