// Package mediatest provides lossless stub codecs and frame generators so
// code that depends on media.Encoder/Decoder can be tested without FFmpeg.
package mediatest

import (
	"context"
	"encoding/binary"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

// Pattern returns a deterministic frame whose bytes depend on seed.
func Pattern(width, height int, seed byte) media.I420Frame {
	f, err := media.NewI420(width, height)
	if err != nil {
		panic(err)
	}
	for i := range f.Data {
		f.Data[i] = byte(i) + seed
	}
	return f
}

// EncoderFactory returns passthrough encoders: the access unit is a 4-byte
// size header followed by the raw frame.
func EncoderFactory(width, height, bitrate int) (media.Encoder, error) {
	return &stubEncoder{}, nil
}

// DecoderFactory returns decoders for EncoderFactory's output.
func DecoderFactory() (media.Decoder, error) { return &stubDecoder{}, nil }

type stubEncoder struct{}

func (*stubEncoder) Encode(_ context.Context, f media.I420Frame, _ bool) ([]byte, error) {
	au := make([]byte, 4, 4+len(f.Data))
	binary.BigEndian.PutUint16(au[0:], uint16(f.Width))
	binary.BigEndian.PutUint16(au[2:], uint16(f.Height))
	return append(au, f.Data...), nil
}
func (*stubEncoder) SetBitrate(int) error { return nil }
func (*stubEncoder) Close() error         { return nil }

type stubDecoder struct{}

func (*stubDecoder) Decode(_ context.Context, au []byte) (media.I420Frame, bool, error) {
	if len(au) < 4 {
		return media.I420Frame{}, false, errors.New("StubDecodeFailed", "access unit too short", "bytes", len(au))
	}
	f := media.I420Frame{
		Width:  int(binary.BigEndian.Uint16(au[0:])),
		Height: int(binary.BigEndian.Uint16(au[2:])),
		Data:   append([]byte(nil), au[4:]...),
	}
	if !f.Valid() {
		return media.I420Frame{}, false, errors.New("StubDecodeFailed", "payload does not match header",
			"width", f.Width, "height", f.Height, "bytes", len(f.Data))
	}
	return f, true, nil
}
func (*stubDecoder) Close() error { return nil }
