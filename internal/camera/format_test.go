package camera_test

import (
	"testing"

	"github.com/rm4n0s/desktop-mirror/internal/camera"
)

func TestBestFormat(t *testing.T) {
	tests := []struct {
		name    string
		formats []camera.FormatInfo
		want    int
	}{
		{"none", nil, -1},
		{"closest to 720p", []camera.FormatInfo{{640, 480, 30}, {1280, 720, 30}, {1920, 1080, 30}, {2592, 1944, 30}}, 1},
		{"fast beats slow", []camera.FormatInfo{{1280, 720, 10}, {640, 480, 30}}, 1},
		{"only slow modes", []camera.FormatInfo{{2592, 1944, 5}, {1280, 720, 10}}, 1},
		{"skips invalid sizes", []camera.FormatInfo{{0, 0, 30}, {1920, 1080, 60}}, 1},
		{"1080p over 5MP", []camera.FormatInfo{{2592, 1944, 30}, {1920, 1080, 30}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := camera.BestFormat(tt.formats); got != tt.want {
				t.Errorf("BestFormat = %d, want %d", got, tt.want)
			}
		})
	}
}
