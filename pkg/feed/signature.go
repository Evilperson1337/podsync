package feed

import (
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/configschema"
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
	Action string `toml:"action" json:"action" enum:"cut_before,cut_after,remove_segment"`
	// PreSeconds is padding before signature_start.
	PreSeconds Number `toml:"pre" json:"pre"`
	// PostSeconds is padding after signature_end.
	PostSeconds Number `toml:"post" json:"post"`
	// MaxMatches is how many occurrences of the signature to act on. Values below 2 use only the
	// strongest match; higher values find repeated occurrences (e.g. a stinger before every ad break).
	MaxMatches int `toml:"max_matches" json:"max_matches,omitempty"`
	// MinScore overrides the minimum confidence score (0-1) for this rule; 0 keeps the default.
	MinScore Number `toml:"min_score" json:"min_score,omitempty"`
	// MinPeakRatio overrides the minimum best/runner-up peak ratio for this rule; 0 keeps the default.
	MinPeakRatio Number `toml:"min_peak_ratio" json:"min_peak_ratio,omitempty"`
}

// Number is a float64 configuration value that also accepts TOML integers, so both
// post = 60 and post = 60.5 work. The TOML decoder does not convert integers to floats itself.
type Number float64

// UnmarshalTOML implements toml.Unmarshaler.
func (n *Number) UnmarshalTOML(value interface{}) error {
	switch v := value.(type) {
	case int64:
		*n = Number(v)
	case float64:
		*n = Number(v)
	default:
		return errors.Errorf("expected a number, got %v (%T)", value, value)
	}
	return nil
}

// ConfigSchema describes Number for the admin interface.
func (Number) ConfigSchema() configschema.Schema {
	return configschema.Schema{Type: "number"}
}

// Seconds converts a number of seconds to a time.Duration.
func (n Number) Seconds() time.Duration {
	return time.Duration(float64(n) * float64(time.Second))
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
