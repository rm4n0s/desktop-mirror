//go:build !cgo || darwin

package ffmpeg_test

import (
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media/ffmpeg"
)

func TestWithoutCgoThereIsNoCodec(t *testing.T) {
	_, err := ffmpeg.NewEncoder(640, 480, 1_000_000)
	appErr, ok := errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("NewEncoder.FFmpegUnavailable") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
	_, err = ffmpeg.NewDecoder()
	appErr, ok = errors.FromError(err)
	if !ok {
		t.Fatalf("expected *errors.Error, got %T", err)
	}
	if !appErr.HasRoute("NewDecoder.FFmpegUnavailable") {
		t.Errorf("unexpected failure path: %s", appErr.Route())
	}
}
