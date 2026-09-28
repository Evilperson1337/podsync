package update

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

func TestManagerSetFeedsAndKeysConcurrently(t *testing.T) {
	storage := newTestLocalStorage(t)
	manager, err := NewUpdater(map[string]*feed.Config{}, map[model.Provider]feed.KeyProvider{}, "http://localhost", "", nil, newTestDB(t), storage)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			manager.SetFeeds(map[string]*feed.Config{"a": {ID: "a", OPML: true}})
			manager.SetKeys(map[model.Provider]feed.KeyProvider{model.ProviderRumble: feed.NewStaticKeyProvider("")})
		}()
		go func() {
			defer wg.Done()
			_ = manager.shouldBuildOPML(&feed.Config{ID: "b"})
			_ = manager.currentKeys()
		}()
	}
	wg.Wait()

	assert.False(t, manager.shouldBuildOPML(&feed.Config{ID: "b"}), "OPML decisions use the reloaded feeds")
	require.NoError(t, manager.BuildOPMLNow(context.Background()))
	opml, err := storage.Open("/podsync.opml")
	require.NoError(t, err)
	defer opml.Close()
}

func TestManagerBuildFeedUsesReloadedKeys(t *testing.T) {
	manager, err := NewUpdater(map[string]*feed.Config{}, map[model.Provider]feed.KeyProvider{}, "http://localhost", "", nil, newTestDB(t), newTestLocalStorage(t))
	require.NoError(t, err)

	_, err = manager.buildFeed(context.Background(), &feed.Config{ID: "x", URL: "https://www.youtube.com/@example"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `key provider "youtube" not loaded`)

	manager.SetKeys(map[model.Provider]feed.KeyProvider{model.ProviderYoutube: feed.NewStaticKeyProvider("k")})
	require.Contains(t, manager.currentKeys(), model.ProviderYoutube, "builds read keys at call time, so the reloaded provider is used")
}
