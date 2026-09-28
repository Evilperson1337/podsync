package feed

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSignatureRuleValidate(t *testing.T) {
	valid := SignatureRule{File: "intro.wav", Action: SignatureActionCutBefore, PostSeconds: 2, MaxMatches: 3, MinScore: 0.7, MinPeakRatio: 1.5}
	assert.NoError(t, valid.Validate())

	invalid := map[string]SignatureRule{
		"missing file":       {Action: SignatureActionCutBefore},
		"unknown action":     {File: "a.wav", Action: "cut_middle"},
		"negative pre":       {File: "a.wav", Action: SignatureActionRemoveSegment, PreSeconds: -1},
		"negative matches":   {File: "a.wav", Action: SignatureActionRemoveSegment, MaxMatches: -1},
		"score above 1":      {File: "a.wav", Action: SignatureActionRemoveSegment, MinScore: 1.5},
		"negative ratio":     {File: "a.wav", Action: SignatureActionRemoveSegment, MinPeakRatio: -0.1},
		"blank file":         {File: "  ", Action: SignatureActionCutAfter},
		"action wrong case":  {File: "a.wav", Action: "Cut_Before"},
		"negative post":      {File: "a.wav", Action: SignatureActionCutAfter, PostSeconds: -2},
		"score below 0":      {File: "a.wav", Action: SignatureActionCutAfter, MinScore: -0.2},
		"empty action field": {File: "a.wav"},
	}
	for name, rule := range invalid {
		assert.Error(t, rule.Validate(), name)
	}
}

func TestSignatureRuleMaxMatchCount(t *testing.T) {
	assert.Equal(t, 1, SignatureRule{}.MaxMatchCount())
	assert.Equal(t, 1, SignatureRule{MaxMatches: 1}.MaxMatchCount())
	assert.Equal(t, 5, SignatureRule{MaxMatches: 5}.MaxMatchCount())
}
