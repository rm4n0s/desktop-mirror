//go:build !cgo

// Package ffmpeg implements media.Encoder and media.Decoder for VP8 on top of libavcodec (cgo). Built without cgo it has no codec:
// NewEncoder and NewDecoder fail with FFmpegUnavailable, so the rest of the program (the window, the signaling server, the WebRTC
// peer, the camera list) still builds and runs on a machine without the FFmpeg development files, and the window says why no video
// is sent or shown.
package ffmpeg

import (
	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

// NewEncoder always fails with the tag FFmpegUnavailable: this build has no cgo and so no libavcodec. It matches
// media.EncoderFactory.
func NewEncoder(width, height, bitrate int) (media.Encoder, error) {
	return nil, errors.New("FFmpegUnavailable", "this build has no FFmpeg: build with cgo, pkg-config and the libavcodec and libavutil development files",
		"width", width, "height", height, "bitrate", bitrate)
}

// NewDecoder always fails with the tag FFmpegUnavailable, as NewEncoder does. It matches media.DecoderFactory.
func NewDecoder() (media.Decoder, error) {
	return nil, errors.New("FFmpegUnavailable", "this build has no FFmpeg: build with cgo, pkg-config and the libavcodec and libavutil development files")
}
