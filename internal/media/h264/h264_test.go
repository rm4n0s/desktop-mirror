package h264_test

import (
	"bytes"
	"testing"

	"github.com/rm4n0s/desktop-mirror/internal/media/h264"
)

func TestSplitAnnexB(t *testing.T) {
	tests := []struct {
		name string
		au   []byte
		want [][]byte
	}{
		{"empty", nil, nil},
		{"no start code", []byte{1, 2, 3, 4}, nil},
		{"one unit, four-byte start code", []byte{0, 0, 0, 1, 0x67, 1, 2}, [][]byte{{0x67, 1, 2}}},
		{"one unit, three-byte start code", []byte{0, 0, 1, 0x68, 9}, [][]byte{{0x68, 9}}},
		{"mixed start codes", []byte{0, 0, 0, 1, 0x67, 1, 0, 0, 1, 0x68, 2, 0, 0, 0, 1, 0x65, 3, 4}, [][]byte{{0x67, 1}, {0x68, 2}, {0x65, 3, 4}}},
		{"bytes before the first start code are left out", []byte{9, 9, 0, 0, 1, 0x41, 5}, [][]byte{{0x41, 5}}},
		{"a start code at the end makes no unit", []byte{0, 0, 1, 0x41, 5, 0, 0, 1}, [][]byte{{0x41, 5}}},
		{"empty units are left out", []byte{0, 0, 1, 0, 0, 1, 0x41}, [][]byte{{0x41}}},
		{"zeros inside a unit stay", []byte{0, 0, 1, 0x41, 0, 7, 0, 0, 3, 1}, [][]byte{{0x41, 0, 7, 0, 0, 3, 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h264.SplitAnnexB(tt.au)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d units %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if !bytes.Equal(got[i], tt.want[i]) {
					t.Errorf("unit %d = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTypeAndKeyframe(t *testing.T) {
	tests := []struct {
		name     string
		nal      []byte
		wantType int
	}{
		{"empty", nil, 0},
		{"sps", []byte{0x67}, h264.NALSPS},
		{"pps", []byte{0x68}, h264.NALPPS},
		{"idr", []byte{0x65}, h264.NALIDR},
		{"slice", []byte{0x41}, h264.NALSlice},
		{"sei", []byte{0x06}, h264.NALSEI},
		{"aud", []byte{0x09}, h264.NALAUD},
	}
	for _, tt := range tests {
		if got := h264.Type(tt.nal); got != tt.wantType {
			t.Errorf("%s: Type = %d, want %d", tt.name, got, tt.wantType)
		}
	}
	key := h264.AppendAnnexB(nil, []byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 3})
	if !h264.IsKeyframe(key) {
		t.Error("an access unit with an IDR slice is not a keyframe")
	}
	if h264.IsKeyframe(h264.AppendAnnexB(nil, []byte{0x41, 3})) {
		t.Error("an access unit with a plain slice is a keyframe")
	}
}

func TestAppendAVCCAndAnnexB(t *testing.T) {
	nals := [][]byte{{0x67, 1, 2}, {0x65}}
	if got, want := h264.AppendAVCC([]byte{0xff}, nals...), []byte{0xff, 0, 0, 0, 3, 0x67, 1, 2, 0, 0, 0, 1, 0x65}; !bytes.Equal(got, want) {
		t.Errorf("AppendAVCC = %v, want %v", got, want)
	}
	annexB := h264.AppendAnnexB(nil, nals...)
	if want := []byte{0, 0, 0, 1, 0x67, 1, 2, 0, 0, 0, 1, 0x65}; !bytes.Equal(annexB, want) {
		t.Errorf("AppendAnnexB = %v, want %v", annexB, want)
	}
	back := h264.SplitAnnexB(annexB)
	if len(back) != 2 || !bytes.Equal(back[0], nals[0]) || !bytes.Equal(back[1], nals[1]) {
		t.Errorf("round trip = %v", back)
	}
}
