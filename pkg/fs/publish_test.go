package fs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublisherPublish(t *testing.T) {
	storage, err := NewLocal(t.TempDir(), false)
	require.NoError(t, err)
	publisher := NewPublisher(storage)

	result, err := publisher.Publish(context.Background(), "feed/item.mp3", bytes.NewBufferString("hello"), PublishOptions{MinSize: 1})
	require.NoError(t, err)
	assert.EqualValues(t, 5, result.Size)
	data, err := os.ReadFile(filepath.Join(storage.RootDir(), "feed", "item.mp3"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestPublisherPublishRejectsTooSmall(t *testing.T) {
	storage, err := NewLocal(t.TempDir(), false)
	require.NoError(t, err)
	publisher := NewPublisher(storage)

	_, err = publisher.Publish(context.Background(), "feed/item.mp3", bytes.NewBufferString("hi"), PublishOptions{MinSize: 3})
	require.Error(t, err)
}
