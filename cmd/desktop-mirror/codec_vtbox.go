//go:build darwin && cgo

package main

import (
	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/vtbox"
)

// On a Mac the video is H.264 through VideoToolbox, which the system has: nothing is installed for it.
const videoCodec = media.CodecH264

var (
	newEncoder media.EncoderFactory = vtbox.NewEncoder
	newDecoder media.DecoderFactory = vtbox.NewDecoder
)
