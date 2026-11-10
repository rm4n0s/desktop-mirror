//go:build !(darwin && cgo)

package main

import (
	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/ffmpeg"
)

// Everywhere but on a Mac the video is VP8 through FFmpeg's libavcodec (a build without cgo has a stub that says so).
const videoCodec = media.CodecVP8

var (
	newEncoder media.EncoderFactory = ffmpeg.NewEncoder
	newDecoder media.DecoderFactory = ffmpeg.NewDecoder
)
