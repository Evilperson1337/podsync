package update

import (
	"testing"
	"time"

	"github.com/mxpv/podsync/pkg/audiosig"
	log "github.com/sirupsen/logrus"
)

func TestBuildTrimPlanMergesRemoveSegments(t *testing.T) {
	matches := []matchedRule{
		{rule: SignatureRule{Action: "remove_segment"}, result: audiosig.Result{SignatureStart: 10 * time.Second, SignatureEnd: 20 * time.Second}},
		{rule: SignatureRule{Action: "remove_segment"}, result: audiosig.Result{SignatureStart: 18 * time.Second, SignatureEnd: 30 * time.Second}},
	}
	keep := buildTrimPlan(60*time.Second, matches, log.New())
	if len(keep) != 2 {
		t.Fatalf("expected 2 keep ranges, got %d", len(keep))
	}
	if keep[0] != (timeRange{start: 0, end: 10 * time.Second}) {
		t.Fatalf("unexpected first keep range: %+v", keep[0])
	}
	if keep[1] != (timeRange{start: 30 * time.Second, end: 60 * time.Second}) {
		t.Fatalf("unexpected second keep range: %+v", keep[1])
	}
}

func TestBuildTrimPlanCombinesBoundaryAndSegmentTrims(t *testing.T) {
	matches := []matchedRule{
		{rule: SignatureRule{Action: "cut_before"}, result: audiosig.Result{SignatureStart: 5 * time.Second, SignatureEnd: 12 * time.Second}},
		{rule: SignatureRule{Action: "remove_segment"}, result: audiosig.Result{SignatureStart: 20 * time.Second, SignatureEnd: 25 * time.Second}},
		{rule: SignatureRule{Action: "remove_segment"}, result: audiosig.Result{SignatureStart: 40 * time.Second, SignatureEnd: 45 * time.Second}},
	}
	keep := buildTrimPlan(60*time.Second, matches, log.New())
	expected := []timeRange{{start: 12 * time.Second, end: 20 * time.Second}, {start: 25 * time.Second, end: 40 * time.Second}, {start: 45 * time.Second, end: 60 * time.Second}}
	if len(keep) != len(expected) {
		t.Fatalf("expected %d keep ranges, got %d", len(expected), len(keep))
	}
	for i := range expected {
		if keep[i] != expected[i] {
			t.Fatalf("unexpected keep range at %d: %+v", i, keep[i])
		}
	}
}
