package update

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
)

func TestPublicationServicePersistsXMLSummary(t *testing.T) {
	database, err := db.NewBadger(&db.Config{Dir: t.TempDir()})
	require.NoError(t, err)
	defer database.Close()

	storage, err := fs.NewLocal(t.TempDir(), false)
	require.NoError(t, err)
	service := NewPublicationService(database, storage, map[string]*feed.Config{}, "https://podsync.test")
	feedConfig := &feed.Config{ID: "sample", Format: model.FormatAudio}
	require.NoError(t, database.AddFeed(context.Background(), "sample", &model.Feed{
		ID:      "sample",
		Format:  model.FormatAudio,
		ItemURL: "https://example.com/channel",
		Episodes: []*model.Episode{{
			ID:          "ep1",
			Title:       "Episode",
			Description: "Description",
			Status:      model.EpisodeStored,
			PubDate:     time.Now().UTC(),
			Size:        10,
		}},
	}))

	require.NoError(t, service.PublishFeedXML(context.Background(), feedConfig))
	summary, err := database.GetPublicationSummary(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sample", summary.LastXMLFeedID)
	assert.Equal(t, 1, summary.XMLBuildCount)
	assert.Equal(t, "xml", summary.LastPublicationType)
	storedFeed, err := database.GetFeed(context.Background(), "sample")
	require.NoError(t, err)
	assert.True(t, storedFeed.LastSuccessAt.IsZero())
}
