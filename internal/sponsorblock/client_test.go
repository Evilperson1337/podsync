package sponsorblock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSkipSegments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("videoID") != "v123" {
			t.Fatalf("unexpected video id: %s", r.URL.Query().Get("videoID"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"category":"intro","segment":[0,12.5]},
			{"category":"sponsor","segment":[30,60]}
		]`))
	}))
	defer server.Close()

	client := NewClient(server.Client())
	client.baseURL = server.URL

	segments, err := client.SkipSegments(context.Background(), "v123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].Category != "intro" || segments[0].End != 12500*time.Millisecond {
		t.Fatalf("unexpected first segment: %+v", segments[0])
	}
}

func TestFilterSegments(t *testing.T) {
	segments := []Segment{
		{Category: "intro", Start: 0, End: 10 * time.Second},
		{Category: "sponsor", Start: 10 * time.Second, End: 20 * time.Second},
	}
	filtered := FilterSegments(segments, []string{"sponsor"})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(filtered))
	}
	if filtered[0].Category != "sponsor" {
		t.Fatalf("unexpected segment: %+v", filtered[0])
	}
}
