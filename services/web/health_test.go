package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/model"
)

func TestHealthCheckReportsFailureCategories(t *testing.T) {
	database, err := db.NewBadger(&db.Config{Dir: t.TempDir()})
	require.NoError(t, err)
	defer database.Close()

	require.NoError(t, database.AddFeed(t.Context(), "feed", &model.Feed{
		ID: "feed",
		Episodes: []*model.Episode{{
			ID:              "ep1",
			Status:          model.EpisodeError,
			LastErrorAt:     time.Now().UTC(),
			FailureCategory: model.FailureCategoryProcessing,
		}},
	}))

	srv := New(Config{}, &mockFileSystem{}, database)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.Handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var health HealthStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &health))
	assert.Equal(t, "unhealthy", health.Status)
	assert.Equal(t, 1, health.FailedEpisodes)
	assert.Equal(t, 1, health.FailureCategories[model.FailureCategoryProcessing])

	summary, err := database.GetHealthSummary(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "unhealthy", summary.Status)
	assert.Equal(t, 1, summary.FailedEpisodes)
}

func TestHealthCheckReportsConfiguredFeeds(t *testing.T) {
	database, err := db.NewBadger(&db.Config{Dir: t.TempDir()})
	require.NoError(t, err)
	defer database.Close()

	feeds := 0
	srv := New(Config{}, &mockFileSystem{}, database)
	srv.SetFeedCount(func() int { return feeds })

	get := func() (int, HealthStatus) {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var health HealthStatus
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &health))
		return rec.Code, health
	}

	code, health := get()
	assert.Equal(t, http.StatusOK, code, "no feeds is a setup state, not a failure")
	assert.Equal(t, "healthy", health.Status)
	require.NotNil(t, health.ConfiguredFeeds)
	assert.Equal(t, 0, *health.ConfiguredFeeds)
	assert.Contains(t, health.Message, "no feeds configured")

	feeds = 3
	code, health = get()
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 3, *health.ConfiguredFeeds)
	assert.NotContains(t, health.Message, "no feeds configured")
}
