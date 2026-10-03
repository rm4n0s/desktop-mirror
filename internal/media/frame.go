// Package media holds the video frame types and codec interfaces shared by
// the camera, WebRTC and UI layers. It has no Qt or cgo dependency.
package media

import (
	"context"

	"github.com/rm4n0s/errors"
)

// I420Frame is a planar YUV 4:2:0 frame stored contiguously as Y, then U,
// then V, with no row padding. Width and Height must be even.
type I420Frame struct {
	Width  int
	Height int
	Data   []byte
}

// RGBAFrame is a packed 8-bit R,G,B,A frame with no row padding.
type RGBAFrame struct {
	Width  int
	Height int
	Data   []byte
}

// I420Size returns the byte size of an I420 frame.
func I420Size(width, height int) int {
	return width*height + 2*((width/2)*(height/2))
}

// NewI420 allocates a zeroed I420 frame.
func NewI420(width, height int) (I420Frame, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return I420Frame{}, errors.New("InvalidFrameSize", "frame dimensions must be positive and even",
			"width", width, "height", height)
	}
	return I420Frame{Width: width, Height: height, Data: make([]byte, I420Size(width, height))}, nil
}

// Planes returns the Y, U and V planes of the frame.
func (f I420Frame) Planes() (y, u, v []byte) {
	ySize := f.Width * f.Height
	cSize := (f.Width / 2) * (f.Height / 2)
	return f.Data[:ySize], f.Data[ySize : ySize+cSize], f.Data[ySize+cSize : ySize+2*cSize]
}

// Valid reports whether the frame's buffer matches its dimensions.
func (f I420Frame) Valid() bool {
	return f.Width > 0 && f.Height > 0 && f.Width%2 == 0 && f.Height%2 == 0 &&
		len(f.Data) == I420Size(f.Width, f.Height)
}

// Encoder compresses raw frames into one codec access unit per call.
type Encoder interface {
	// Encode compresses a frame. The result may be empty if the codec
	// buffered the frame. forceKeyframe requests an intra frame.
	Encode(ctx context.Context, f I420Frame, forceKeyframe bool) ([]byte, error)
	SetBitrate(bps int) error
	Close() error
}

// Decoder turns one codec access unit into a raw frame.
type Decoder interface {
	// Decode returns ok=false when the access unit produced no picture yet.
	Decode(ctx context.Context, au []byte) (frame I420Frame, ok bool, err error)
	Close() error
}

// EncoderFactory creates an encoder for the given frame size and bitrate.
type EncoderFactory func(width, height, bitrate int) (Encoder, error)

// DecoderFactory creates a decoder.
type DecoderFactory func() (Decoder, error)
