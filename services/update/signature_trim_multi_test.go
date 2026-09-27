package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

const testSignatureExpr = "0.8*sin(2*PI*440*t)*abs(sin(2*PI*1.3*t))+0.4*sin(2*PI*1250*t)*gt(t,1.7)"

// writeEpisodeWithSignatures renders quiet noise with a 3 second signature at each offset.
func writeEpisodeWithSignatures(t *testing.T, total time.Duration, offsets ...time.Duration) string {
	t.Helper()
	const sigDur = 3 * time.Second
	var (
		args   []string
		labels []string
		cursor time.Duration
	)
	add := func(source string) {
		labels = append(labels, fmt.Sprintf("[%d:a]", len(labels)))
		args = append(args, "-f", "lavfi", "-i", source)
	}
	for i, offset := range offsets {
		add(fmt.Sprintf("anoisesrc=c=pink:a=0.05:r=22050:d=%g:seed=%d", (offset - cursor).Seconds(), 10+i))
		add(fmt.Sprintf("aevalsrc='%s':s=22050:d=%g", testSignatureExpr, sigDur.Seconds()))
		cursor = offset + sigDur
	}
	add(fmt.Sprintf("anoisesrc=c=pink:a=0.05:r=22050:d=%g:seed=99", (total - cursor).Seconds()))
	args = append(args,
		"-filter_complex", strings.Join(labels, "")+fmt.Sprintf("concat=n=%d:v=0:a=1[out]", len(labels)),
		"-map", "[out]", "-c:a", "libmp3lame", "-q:a", "4")
	return generateMedia(t, "episode.mp3", args...)
}

func setupSignatureRules(t *testing.T, feedID string, rules string) string {
	t.Helper()
	root := t.TempDir()
	sigDir := filepath.Join(root, feedID, "signatures")
	require.NoError(t, os.MkdirAll(sigDir, 0755))
	generated := generateMedia(t, "signature.wav", "-f", "lavfi", "-i", fmt.Sprintf("aevalsrc='%s':s=22050:d=3", testSignatureExpr))
	data, err := os.ReadFile(generated)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sigDir, "signature.wav"), data, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sigDir, "rules.json"), []byte(rules), 0644))
	return root
}

func TestSignatureTrimRemovesRepeatedSegments(t *testing.T) {
	requireFFmpeg(t)
	input := writeEpisodeWithSignatures(t, 100*time.Second, 10*time.Second, 40*time.Second, 75*time.Second)

	tests := []struct {
		name     string
		rules    string
		expected time.Duration
	}{
		{
			name:     "max_matches removes every occurrence",
			rules:    `{"rules":[{"file":"signature.wav","action":"remove_segment","max_matches":10}]}`,
			expected: 91 * time.Second,
		},
		{
			name:     "default removes only the strongest occurrence",
			rules:    `{"rules":[{"file":"signature.wav","action":"remove_segment"}]}`,
			expected: 97 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			feedConfig := &feed.Config{ID: "show", Format: model.FormatAudio}
			manager := &Manager{sigDir: setupSignatureRules(t, feedConfig.ID, tt.rules)}

			source, err := os.Open(input)
			require.NoError(t, err)
			defer source.Close()

			reader, cleanup, err := manager.trimEpisodeIfSignatureFound(ctx, feedConfig, &model.Episode{ID: "ep"}, source)
			require.NoError(t, err)
			if cleanup != nil {
				defer cleanup()
			}
			named, ok := reader.(interface{ Name() string })
			require.True(t, ok)
			require.NotEqual(t, input, named.Name(), "episode should have been trimmed")

			duration := resultDurationOrZero(ctx, named.Name(), log.New())
			assert.InDelta(t, float64(tt.expected), float64(duration), float64(500*time.Millisecond), "duration %s", duration)
		})
	}
}
