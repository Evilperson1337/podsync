package update

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOPMLPublisherDebouncesRequests(t *testing.T) {
	var builds int32
	publisher := NewOPMLPublisher(func(ctx context.Context) error {
		atomic.AddInt32(&builds, 1)
		return nil
	}, 25*time.Millisecond)

	publisher.Request(context.Background())
	publisher.Request(context.Background())
	publisher.Request(context.Background())

	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&builds))
}

func TestOPMLPublisherFlushBuildsImmediately(t *testing.T) {
	var builds int32
	publisher := NewOPMLPublisher(func(ctx context.Context) error {
		atomic.AddInt32(&builds, 1)
		return nil
	}, time.Minute)

	publisher.Request(context.Background())
	require.NoError(t, publisher.Flush(context.Background()))
	assert.Equal(t, int32(1), atomic.LoadInt32(&builds))
}
