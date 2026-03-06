package update

import (
	"encoding/json"
	"fmt"
	"os"
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

// SignatureRule defines a single signature action.
// Inputs:
// - File: signature filename within the signatures directory.
// - Action: cut_before | cut_after | remove_segment.
// - PreSeconds: seconds before signature_start for padding.
// - PostSeconds: seconds after signature_end for padding.
// Outputs: none.
// Example usage:
//
//	SignatureRule{File: "intro.wav", Action: "cut_before", PostSeconds: 0}
//
// Notes: Pre/Post are applied per action semantics.
type SignatureRule struct {
	File        string  `json:"file"`
	Action      string  `json:"action"`
	PreSeconds  float64 `json:"pre"`
	PostSeconds float64 `json:"post"`
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
