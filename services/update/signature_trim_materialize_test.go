package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaterializeTrimInputReusesNamedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episode.mp3")
	require.NoError(t, os.WriteFile(path, []byte("audio"), 0o644))
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()

	inputPath, inputBytes, cleanup, reused, err := materializeTrimInput(file)
	require.NoError(t, err)
	assert.True(t, reused)
	assert.Equal(t, path, inputPath)
	assert.EqualValues(t, 5, inputBytes)
	assert.Nil(t, cleanup)
}

func TestMaterializeTrimInputCopiesUnnamedReader(t *testing.T) {
	inputPath, inputBytes, cleanup, reused, err := materializeTrimInput(strings.NewReader("audio"))
	require.NoError(t, err)
	assert.False(t, reused)
	assert.EqualValues(t, 5, inputBytes)
	require.NotNil(t, cleanup)
	_, statErr := os.Stat(inputPath)
	require.NoError(t, statErr)
	cleanup()
	_, statErr = os.Stat(inputPath)
	assert.True(t, os.IsNotExist(statErr))
}
