package model

import "time"

type HealthSummary struct {
	Status            string         `json:"status"`
	Timestamp         time.Time      `json:"timestamp"`
	FailedEpisodes    int            `json:"failed_episodes,omitempty"`
	FailureCategories map[string]int `json:"failure_categories,omitempty"`
	Message           string         `json:"message,omitempty"`
	ComputedAt        time.Time      `json:"computed_at"`
}

type PublicationSummary struct {
	LastXMLBuildAt      time.Time `json:"last_xml_build_at,omitempty"`
	LastXMLFeedID       string    `json:"last_xml_feed_id,omitempty"`
	XMLBuildCount       int       `json:"xml_build_count,omitempty"`
	LastOPMLBuildAt     time.Time `json:"last_opml_build_at,omitempty"`
	OPMLBuildCount      int       `json:"opml_build_count,omitempty"`
	LastPublicationAt   time.Time `json:"last_publication_at,omitempty"`
	LastPublicationType string    `json:"last_publication_type,omitempty"`
}
