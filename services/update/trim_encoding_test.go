package update

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/audiosig"
)

func TestSelectTrimEncoding(t *testing.T) {
	tests := []struct {
		name      string
		extension string
		streams   mediaStreams
		encoder   string
		copy      bool
	}{
		{name: "mp3 audio", extension: ".mp3", streams: mediaStreams{audioCodec: "mp3"}, encoder: "libmp3lame"},
		{name: "m4a aac", extension: ".m4a", streams: mediaStreams{audioCodec: "aac"}, encoder: "aac"},
		{name: "opus", extension: ".opus", streams: mediaStreams{audioCodec: "opus"}, encoder: "libopus"},
		{name: "flac", extension: ".flac", streams: mediaStreams{audioCodec: "flac"}, encoder: "flac"},
		{name: "video is stream-copied", extension: ".mp4", streams: mediaStreams{hasVideo: true, audioCodec: "aac"}, copy: true},
		{name: "unknown audio codec is stream-copied", extension: ".wma", streams: mediaStreams{audioCodec: "wmav2"}, copy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoding := selectTrimEncoding(tt.extension, tt.streams)
			assert.Equal(t, tt.extension, encoding.extension)
			assert.Equal(t, tt.encoder, encoding.encoder)
			assert.Equal(t, tt.copy, encoding.streamCopy())
		})
	}
}

func TestTrimEncodingCodecArgs(t *testing.T) {
	mp3 := selectTrimEncoding(".mp3", mediaStreams{audioCodec: "mp3"})
	assert.Equal(t, []string{"-c:a", "libmp3lame", "-b:a", "128k"}, mp3.codecArgs(128), "mp3 arguments are unchanged from the original trim path")
	assert.Equal(t, []string{"-c:a", "libmp3lame", "-q:a", "2"}, mp3.codecArgs(0))

	aac := selectTrimEncoding(".m4a", mediaStreams{audioCodec: "aac"})
	assert.Equal(t, []string{"-map", "0:a", "-c:a", "aac", "-b:a", "160k"}, aac.codecArgs(160))
	assert.Equal(t, []string{"-map", "0:a", "-c:a", "aac"}, aac.codecArgs(0))

	flac := selectTrimEncoding(".flac", mediaStreams{audioCodec: "flac"})
	assert.Equal(t, []string{"-map", "0:a", "-c:a", "flac"}, flac.codecArgs(900))

	video := selectTrimEncoding(".mp4", mediaStreams{hasVideo: true, audioCodec: "aac"})
	assert.Equal(t, []string{"-map", "0:v?", "-map", "0:a?", "-c", "copy", "-avoid_negative_ts", "make_zero"}, video.codecArgs(128))
}

func TestParseMediaStreams(t *testing.T) {
	streams, err := parseMediaStreams([]byte(`{"streams":[
		{"codec_type":"audio","codec_name":"AAC","disposition":{"attached_pic":0}},
		{"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}}
	]}`))
	require.NoError(t, err)
	assert.False(t, streams.hasVideo, "embedded cover art is not a video stream")
	assert.Equal(t, "aac", streams.audioCodec)

	streams, err = parseMediaStreams([]byte(`{"streams":[
		{"codec_type":"video","codec_name":"h264","disposition":{"attached_pic":0}},
		{"codec_type":"audio","codec_name":"aac","disposition":{"attached_pic":0}}
	]}`))
	require.NoError(t, err)
	assert.True(t, streams.hasVideo)
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func generateMedia(t *testing.T, name string, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	full := append([]string{"-y", "-v", "error", "-nostdin"}, args...)
	full = append(full, path)
	output, err := exec.Command("ffmpeg", full...).CombinedOutput()
	require.NoError(t, err, string(output))
	return path
}

func TestApplyMatchedRulesPreservesFormat(t *testing.T) {
	requireFFmpeg(t)
	const sine = "sine=frequency=440:duration=20"

	tests := []struct {
		name      string
		file      string
		args      []string
		wantVideo bool
		wantCodec string
		tolerance time.Duration
	}{
		{name: "mp3", file: "in.mp3", args: []string{"-f", "lavfi", "-i", sine, "-c:a", "libmp3lame"}, wantCodec: "mp3", tolerance: 500 * time.Millisecond},
		{name: "m4a", file: "in.m4a", args: []string{"-f", "lavfi", "-i", sine, "-c:a", "aac"}, wantCodec: "aac", tolerance: 500 * time.Millisecond},
		{name: "opus", file: "in.opus", args: []string{"-f", "lavfi", "-i", sine, "-c:a", "libopus"}, wantCodec: "opus", tolerance: 500 * time.Millisecond},
		{
			name: "mp4 video",
			file: "in.mp4",
			args: []string{
				"-f", "lavfi", "-i", "testsrc=duration=20:size=160x120:rate=25",
				"-f", "lavfi", "-i", sine,
				"-c:v", "libx264", "-g", "25", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest",
			},
			wantVideo: true,
			wantCodec: "aac",
			// Stream copy cuts on keyframes (every second here).
			tolerance: 2 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			input := generateMedia(t, tt.file, tt.args...)
			matches := []matchedRule{{
				rule:   SignatureRule{Action: "remove_segment"},
				result: audiosig.Result{SignatureStart: 5 * time.Second, SignatureEnd: 10 * time.Second},
			}}

			output, cleanup, err := (&Manager{}).applyMatchedRules(ctx, input, 20*time.Second, matches, filepath.Ext(tt.file), log.New())
			require.NoError(t, err)
			defer cleanup()
			require.NotEqual(t, input, output, "trim should produce a new file")
			assert.Equal(t, filepath.Ext(tt.file), filepath.Ext(output))

			streams, err := probeMediaStreams(ctx, output)
			require.NoError(t, err)
			assert.Equal(t, tt.wantVideo, streams.hasVideo)
			assert.Equal(t, tt.wantCodec, streams.audioCodec)

			duration := resultDurationOrZero(ctx, output, log.New())
			assert.InDelta(t, float64(15*time.Second), float64(duration), float64(tt.tolerance), "duration %s", duration)
		})
	}
}
