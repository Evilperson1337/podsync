package update

import (
	"testing"

	"github.com/mxpv/podsync/pkg/model"
)

func TestSponsorBlockVideoID(t *testing.T) {
	if got := sponsorBlockVideoID(&model.Episode{ID: "v76ws1m"}); got != "v76ws1m" {
		t.Fatalf("unexpected id from episode id: %s", got)
	}
	if got := sponsorBlockVideoID(&model.Episode{VideoURL: "https://rumble.com/v76ws1m-crowder.html"}); got != "v76ws1m" {
		t.Fatalf("unexpected id from video url: %s", got)
	}
}
