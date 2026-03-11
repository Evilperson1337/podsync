package builder

import (
	"time"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

func newFeedModel(info model.Info, cfg *feed.Config) *model.Feed {
	feedModel := &model.Feed{
		ItemID:          info.ItemID,
		Provider:        info.Provider,
		LinkType:        info.LinkType,
		Format:          cfg.Format,
		Quality:         cfg.Quality,
		CoverArtQuality: cfg.Custom.CoverArtQuality,
		PageSize:        cfg.PageSize,
		PlaylistSort:    cfg.PlaylistSort,
		PrivateFeed:     cfg.PrivateFeed,
		UpdatedAt:       time.Now().UTC(),
	}
	if feedModel.PageSize == 0 {
		feedModel.PageSize = model.DefaultPageSize
	}
	return feedModel
}
