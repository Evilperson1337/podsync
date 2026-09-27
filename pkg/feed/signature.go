package feed

import (
	"slices"
	"strings"

	"github.com/pkg/errors"
)

// Signature trim actions.
const (
	SignatureActionCutBefore     = "cut_before"
	SignatureActionCutAfter      = "cut_after"
	SignatureActionRemoveSegment = "remove_segment"
)

// ValidSignatureActions lists the supported signature trim actions.
func ValidSignatureActions() []string {
	return []string{SignatureActionCutBefore, SignatureActionCutAfter, SignatureActionRemoveSegment}
}

// SignatureRule defines a single signature trim action. It is configured per feed in TOML
//
//	[[feeds.ID.signature_rules]]
//	file = "intro.wav"
//	action = "cut_before"
//
// or, as a fallback, in <signatures_root>/<feed_id>/signatures/rules.json.
type SignatureRule struct {
	// File is the signature audio file, relative to <signatures_root>/<feed_id>/signatures/ or absolute.
	File string `toml:"file" json:"file"`
	// Action is one of cut_before, cut_after, remove_segment.
	Action string `toml:"action" json:"action"`
	// PreSeconds is padding before signature_start.
	PreSeconds float64 `toml:"pre" json:"pre"`
	// PostSeconds is padding after signature_end.
	PostSeconds float64 `toml:"post" json:"post"`
	// MaxMatches is how many occurrences of the signature to act on. Values below 2 use only the
	// strongest match; higher values find repeated occurrences (e.g. a stinger before every ad break).
	MaxMatches int `toml:"max_matches" json:"max_matches,omitempty"`
	// MinScore overrides the minimum confidence score (0-1) for this rule; 0 keeps the default.
	MinScore float64 `toml:"min_score" json:"min_score,omitempty"`
	// MinPeakRatio overrides the minimum best/runner-up peak ratio for this rule; 0 keeps the default.
	MinPeakRatio float64 `toml:"min_peak_ratio" json:"min_peak_ratio,omitempty"`
}

// MaxMatchCount returns how many occurrences to act on, defaulting to 1.
func (r SignatureRule) MaxMatchCount() int {
	if r.MaxMatches < 1 {
		return 1
	}
	return r.MaxMatches
}

// Validate checks the rule's fields. It does not check that the signature file exists.
func (r SignatureRule) Validate() error {
	if strings.TrimSpace(r.File) == "" {
		return errors.New("file is required")
	}
	if !slices.Contains(ValidSignatureActions(), r.Action) {
		return errors.Errorf("action %q must be one of %s", r.Action, strings.Join(ValidSignatureActions(), ", "))
	}
	if r.PreSeconds < 0 || r.PostSeconds < 0 {
		return errors.New("pre and post must not be negative")
	}
	if r.MaxMatches < 0 {
		return errors.New("max_matches must not be negative")
	}
	if r.MinScore < 0 || r.MinScore > 1 {
		return errors.Errorf("min_score %g must be between 0 and 1", r.MinScore)
	}
	if r.MinPeakRatio < 0 {
		return errors.Errorf("min_peak_ratio %g must not be negative", r.MinPeakRatio)
	}
	return nil
}
