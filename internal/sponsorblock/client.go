package sponsorblock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"
)

const defaultBaseURL = "https://sponsor.ajay.app"

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Segment struct {
	Category string
	Start    time.Duration
	End      time.Duration
}

type apiSegment struct {
	Category string    `json:"category"`
	Segment  []float64 `json:"segment"`
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: defaultBaseURL, httpClient: httpClient}
}

func (c *Client) SkipSegments(ctx context.Context, videoID string) ([]Segment, error) {
	if videoID == "" {
		return nil, fmt.Errorf("video id is required")
	}
	endpoint := c.baseURL + "/api/skipSegments?videoID=" + url.QueryEscape(videoID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PodsyncSponsorBlock/1.0 (+https://github.com/mxpv/podsync)")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected sponsorblock status: %d", resp.StatusCode)
	}

	var payload []apiSegment
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	segments := make([]Segment, 0, len(payload))
	for _, item := range payload {
		if len(item.Segment) < 2 {
			return nil, fmt.Errorf("malformed sponsorblock segment for category %q", item.Category)
		}
		start := time.Duration(item.Segment[0] * float64(time.Second))
		end := time.Duration(item.Segment[1] * float64(time.Second))
		segments = append(segments, Segment{Category: item.Category, Start: start, End: end})
	}

	sort.Slice(segments, func(i, j int) bool {
		if segments[i].Start == segments[j].Start {
			return segments[i].End < segments[j].End
		}
		return segments[i].Start < segments[j].Start
	})

	return segments, nil
}

func FilterSegments(segments []Segment, categories []string) []Segment {
	if len(segments) == 0 || len(categories) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		allowed[category] = struct{}{}
	}
	result := make([]Segment, 0, len(segments))
	for _, segment := range segments {
		if _, ok := allowed[segment.Category]; ok {
			result = append(result, segment)
		}
	}
	return result
}
