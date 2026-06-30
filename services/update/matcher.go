package update

import (
	"fmt"
	"regexp"
	"time"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
	log "github.com/sirupsen/logrus"
)

type filterDecision struct {
	Matched     bool
	ReasonCode  string
	Reason      string
	Source      string
	Diagnostics map[string]string
}

func matchRegexpFilter(pattern, value, title, fieldLabel string, negative bool, logger log.FieldLogger) bool {
	if pattern != "" {
		matched, err := regexp.MatchString(pattern, value)
		if err != nil {
			logger.WithError(err).Warnf("Configured regex condition %q for %s is invalid; item %q was not excluded by this invalid rule", pattern, fieldLabel, title)
		} else {
			if matched == negative {
				if negative {
					logger.Infof("Video Name: %q matched the excluded %s regex condition %q and was skipped", title, fieldLabel, pattern)
				} else {
					logger.Infof("Video Name: %q did not meet the %s regex condition %q and was skipped", title, fieldLabel, pattern)
				}
				return false
			}
		}
	}
	return true
}

func matchFilters(episode *model.Episode, filters *feed.Filters) bool {
	return matchFiltersWithDecision(episode, filters).Matched
}

func matchFiltersWithDecision(episode *model.Episode, filters *feed.Filters) filterDecision {
	logger := log.WithFields(log.Fields{"episode_id": episode.ID})
	if !matchRegexpFilter(filters.Title, episode.Title, episode.Title, "title", false, logger.WithField("filter", "title")) {
		return filterDecision{Matched: false, ReasonCode: model.ReasonExcludedByPattern, Reason: "title does not match configured include pattern", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"filter": "title", "pattern": filters.Title}}
	}

	if !matchRegexpFilter(filters.NotTitle, episode.Title, episode.Title, "title", true, logger.WithField("filter", "not_title")) {
		return filterDecision{Matched: false, ReasonCode: model.ReasonExcludedByPattern, Reason: "title matches configured exclude pattern", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"filter": "not_title", "pattern": filters.NotTitle}}
	}

	if !matchRegexpFilter(filters.Description, episode.Description, episode.Title, "description", false, logger.WithField("filter", "description")) {
		return filterDecision{Matched: false, ReasonCode: model.ReasonExcludedByPattern, Reason: "description does not match configured include pattern", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"filter": "description", "pattern": filters.Description}}
	}

	if !matchRegexpFilter(filters.NotDescription, episode.Description, episode.Title, "description", true, logger.WithField("filter", "not_description")) {
		return filterDecision{Matched: false, ReasonCode: model.ReasonExcludedByPattern, Reason: "description matches configured exclude pattern", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"filter": "not_description", "pattern": filters.NotDescription}}
	}

	if filters.MaxDuration > 0 && episode.Duration > filters.MaxDuration {
		logger.WithField("filter", "max_duration").Infof("Video Name: %q is %ds long, which is above the configured maximum duration of %ds, and was skipped", episode.Title, episode.Duration, filters.MaxDuration)
		return filterDecision{Matched: false, ReasonCode: model.ReasonDurationAboveMaximum, Reason: "duration is above configured maximum", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"duration_seconds": fmt.Sprint(episode.Duration), "max_duration_seconds": fmt.Sprint(filters.MaxDuration)}}
	}

	if filters.MinDuration > 0 && episode.Duration < filters.MinDuration {
		logger.WithField("filter", "min_duration").Infof("Video Name: %q is %ds long, which is below the configured minimum duration of %ds, and was skipped", episode.Title, episode.Duration, filters.MinDuration)
		return filterDecision{Matched: false, ReasonCode: model.ReasonDurationBelowMinimum, Reason: "duration is below configured minimum", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"duration_seconds": fmt.Sprint(episode.Duration), "min_duration_seconds": fmt.Sprint(filters.MinDuration)}}
	}

	if filters.MaxAge > 0 {
		dateDiff := int(time.Since(episode.PubDate).Hours()) / 24
		if dateDiff > filters.MaxAge {
			logger.WithField("filter", "max_age").Infof("Video Name: %q is %d days old, which is older than the configured maximum age of %d days, and was skipped", episode.Title, dateDiff, filters.MaxAge)
			return filterDecision{Matched: false, ReasonCode: model.ReasonExcludedByConfig, Reason: "published date is older than configured maximum age", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"age_days": fmt.Sprint(dateDiff), "max_age_days": fmt.Sprint(filters.MaxAge)}}
		}
	}

	if filters.MinAge > 0 {
		dateDiff := int(time.Since(episode.PubDate).Hours()) / 24
		if dateDiff < filters.MinAge {
			logger.WithField("filter", "min_age").Infof("Video Name: %q is %d days old, which is newer than the configured minimum age of %d days, and was skipped", episode.Title, dateDiff, filters.MinAge)
			return filterDecision{Matched: false, ReasonCode: model.ReasonExcludedByConfig, Reason: "published date is newer than configured minimum age", Source: model.DecisionSourceConfiguration, Diagnostics: map[string]string{"age_days": fmt.Sprint(dateDiff), "min_age_days": fmt.Sprint(filters.MinAge)}}
		}
	}

	return filterDecision{Matched: true}
}
