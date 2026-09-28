package update

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// trimEncoding describes how trimmed segments are written so the result keeps the
// container and codecs of the media Podsync publishes for the feed.
type trimEncoding struct {
	// extension is the output file extension including the dot, e.g. ".m4a".
	extension string
	// encoder is the ffmpeg audio encoder; "" means stream copy of all audio/video streams.
	encoder string
	// lossy reports whether the source bitrate should be carried over to the encoder.
	lossy bool
}

type audioEncoder struct {
	name  string
	lossy bool
}

// audioEncoders maps an ffprobe audio codec name to the encoder that reproduces it.
var audioEncoders = map[string]audioEncoder{
	"mp3":       {name: "libmp3lame", lossy: true},
	"aac":       {name: "aac", lossy: true},
	"opus":      {name: "libopus", lossy: true},
	"vorbis":    {name: "libvorbis", lossy: true},
	"flac":      {name: "flac"},
	"alac":      {name: "alac"},
	"pcm_s16le": {name: "pcm_s16le"},
}

type mediaStreams struct {
	// hasVideo is true for real video streams; embedded cover art does not count.
	hasVideo   bool
	audioCodec string
}

// selectTrimEncoding picks how to write trimmed output.
//
// Audio-only media is re-encoded with its original codec for sample-accurate cuts. Media with a
// video stream is stream-copied: re-encoding video is too expensive, so cuts land on the nearest
// keyframe. Unknown audio codecs are stream-copied rather than converted to a different format.
func selectTrimEncoding(extension string, streams mediaStreams) trimEncoding {
	encoding := trimEncoding{extension: extension}
	if streams.hasVideo {
		return encoding
	}
	if encoder, ok := audioEncoders[streams.audioCodec]; ok {
		encoding.encoder = encoder.name
		encoding.lossy = encoder.lossy
	}
	return encoding
}

// streamCopy reports whether segments are cut without re-encoding.
func (e trimEncoding) streamCopy() bool {
	return e.encoder == ""
}

// codecArgs returns the ffmpeg output arguments for one trimmed segment.
func (e trimEncoding) codecArgs(bitrateKbps int) []string {
	if e.streamCopy() {
		return []string{"-map", "0:v?", "-map", "0:a?", "-c", "copy", "-avoid_negative_ts", "make_zero"}
	}
	var args []string
	if e.encoder != "libmp3lame" {
		// Only keep audio: containers such as m4a/ogg cannot re-encode embedded cover art implicitly.
		args = append(args, "-map", "0:a")
	}
	args = append(args, "-c:a", e.encoder)
	switch {
	case e.lossy && bitrateKbps > 0:
		args = append(args, "-b:a", fmt.Sprintf("%dk", bitrateKbps))
	case e.encoder == "libmp3lame":
		args = append(args, "-q:a", "2")
	}
	return args
}

// probeMediaStreams inspects the media streams in inputPath with ffprobe.
func probeMediaStreams(ctx context.Context, inputPath string) (mediaStreams, error) {
	cmd := execCommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "stream=codec_type,codec_name:stream_disposition=attached_pic",
		"-of", "json",
		inputPath,
	)
	output, err := cmd.Output()
	if err != nil {
		return mediaStreams{}, fmt.Errorf("ffprobe streams: %w", err)
	}
	return parseMediaStreams(output)
}

func parseMediaStreams(data []byte) (mediaStreams, error) {
	var payload struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			CodecName   string `json:"codec_name"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return mediaStreams{}, fmt.Errorf("parse ffprobe streams: %w", err)
	}
	var streams mediaStreams
	for _, stream := range payload.Streams {
		switch stream.CodecType {
		case "video":
			if stream.Disposition.AttachedPic == 0 {
				streams.hasVideo = true
			}
		case "audio":
			if streams.audioCodec == "" {
				streams.audioCodec = strings.ToLower(stream.CodecName)
			}
		}
	}
	return streams, nil
}
