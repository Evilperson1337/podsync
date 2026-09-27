package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mxpv/podsync/pkg/feed"
)

// SignatureRules defines a rules.json payload for signature actions.
// Inputs: none (struct definition).
// Outputs: none.
// Example usage:
//
//	rules := SignatureRules{Rules: []SignatureRule{{File: "intro.wav", Action: "cut_before"}}}
//
// Notes: Action must be one of cut_before, cut_after, remove_segment.
type SignatureRules struct {
	Rules []SignatureRule `json:"rules"`
}

// SignatureRule defines a single signature action; see feed.SignatureRule.
type SignatureRule = feed.SignatureRule

// Signature rule sources, for logging.
const (
	SignatureRulesSourceConfig    = "config"
	SignatureRulesSourceRulesJSON = "rules.json"
)

// ResolveSignaturesRoot returns the directory holding per-feed signature folders
// (<root>/<feed_id>/signatures). Precedence: the configured [signatures] root_dir, then the
// PODSYNC_SIGNATURES_DIR environment variable, then the local storage data directory.
// It returns "" when none applies (for example, S3 storage without an explicit root).
func ResolveSignaturesRoot(configured string, localDataDir string) string {
	if root := strings.TrimSpace(configured); root != "" {
		return root
	}
	if root := strings.TrimSpace(os.Getenv("PODSYNC_SIGNATURES_DIR")); root != "" {
		return root
	}
	return strings.TrimSpace(localDataDir)
}

// SignaturesDir returns a feed's signatures directory under a signatures root.
func SignaturesDir(root string, feedID string) string {
	return filepath.Join(root, feedID, "signatures")
}

// SignatureRulesPath returns the rules.json path for a feed under a signatures root.
func SignatureRulesPath(root string, feedID string) string {
	return filepath.Join(SignaturesDir(root, feedID), "rules.json")
}

// SignatureFilePath resolves a rule's signature file: absolute paths are used as-is, relative
// paths are resolved against the feed's signatures directory. It returns "" when a relative
// file has no signatures root to resolve against.
func SignatureFilePath(root string, feedID string, file string) string {
	if filepath.IsAbs(file) {
		return filepath.Clean(file)
	}
	if root == "" {
		return ""
	}
	return filepath.Join(SignaturesDir(root, feedID), file)
}

// LoadFeedSignatureRules returns the signature rules for a feed and where they came from.
// Rules configured in TOML take precedence; otherwise rules.json under the signatures root is read.
// rulesJSONIgnored reports that a rules.json exists but was ignored because TOML rules are set.
func LoadFeedSignatureRules(root string, feedConfig *feed.Config) (rules []SignatureRule, source string, rulesJSONIgnored bool, err error) {
	rulesPath := ""
	if root != "" {
		rulesPath = SignatureRulesPath(root, feedConfig.ID)
	}
	if len(feedConfig.SignatureRules) > 0 {
		if rulesPath != "" {
			if _, statErr := os.Stat(rulesPath); statErr == nil {
				rulesJSONIgnored = true
			}
		}
		return feedConfig.SignatureRules, SignatureRulesSourceConfig, rulesJSONIgnored, nil
	}
	if rulesPath == "" {
		return nil, "", false, nil
	}
	parsed, ok, err := ReadSignatureRules(rulesPath)
	if err != nil || !ok {
		return nil, "", false, err
	}
	return parsed.Rules, SignatureRulesSourceRulesJSON, false, nil
}

// ReadSignatureRules loads rules.json if it exists.
// Inputs: rulesPath.
// Outputs: rules, ok (true if file found), error.
// Example usage:
//
//	rules, ok, err := ReadSignatureRules("/app/data/crowder/signatures/rules.json")
//
// Notes: Returns ok=false when file is missing.
func ReadSignatureRules(rulesPath string) (SignatureRules, bool, error) {
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return SignatureRules{}, false, nil
		}
		return SignatureRules{}, false, fmt.Errorf("read rules: %w", err)
	}
	var rules SignatureRules
	if err := json.Unmarshal(data, &rules); err != nil {
		return SignatureRules{}, false, fmt.Errorf("parse rules: %w", err)
	}
	return rules, true, nil
}
