package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

func TestNewFeedModelCopiesCommonContractFields(t *testing.T) {
	info := model.Info{ItemID: "channel", Provider: model.ProviderYoutube, LinkType: model.TypeChannel}
	cfg := &feed.Config{
		Format:       model.FormatAudio,
		Quality:      model.QualityLow,
		PageSize:     7,
		PlaylistSort: model.SortingDesc,
		PrivateFeed:  true,
		Custom:       feed.Custom{CoverArtQuality: model.QualityHigh},
	}

	feedModel := newFeedModel(info, cfg)
	assert.Equal(t, info.ItemID, feedModel.ItemID)
	assert.Equal(t, info.Provider, feedModel.Provider)
	assert.Equal(t, info.LinkType, feedModel.LinkType)
	assert.Equal(t, cfg.Format, feedModel.Format)
	assert.Equal(t, cfg.Quality, feedModel.Quality)
	assert.Equal(t, cfg.PageSize, feedModel.PageSize)
	assert.Equal(t, cfg.PlaylistSort, feedModel.PlaylistSort)
	assert.Equal(t, cfg.PrivateFeed, feedModel.PrivateFeed)
	assert.Equal(t, cfg.Custom.CoverArtQuality, feedModel.CoverArtQuality)
}
