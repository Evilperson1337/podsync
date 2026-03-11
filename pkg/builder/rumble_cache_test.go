package builder

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

func TestNewFeedModelDefaults(t *testing.T) {
	info := model.Info{ItemID: "id", Provider: model.ProviderYoutube, LinkType: model.TypeChannel}
	feedModel := newFeedModel(info, &feed.Config{})
	assert.Equal(t, model.DefaultPageSize, feedModel.PageSize)
	assert.Equal(t, info.Provider, feedModel.Provider)
	assert.Equal(t, info.LinkType, feedModel.LinkType)
}

func TestRumbleCacheEvictsOldestEntries(t *testing.T) {
	builder := &RumbleBuilder{cache: map[string]rumbleCacheEntry{}}
	now := time.Now().UTC()
	for i := 0; i < rumbleCacheMaxEntries+2; i++ {
		builder.cache[fmt.Sprintf("k-%d", i)] = rumbleCacheEntry{fetchedAt: now.Add(time.Duration(i) * time.Second), lastUsed: now.Add(time.Duration(i) * time.Second)}
	}
	builder.evictCacheLocked()
	builder.evictCacheLocked()
	assert.LessOrEqual(t, len(builder.cache), rumbleCacheMaxEntries)
	_, ok := builder.cache["k-0"]
	assert.False(t, ok)
	_, ok = builder.cache["k-1"]
	assert.False(t, ok)
}
