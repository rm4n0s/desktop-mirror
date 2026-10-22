package camera

import (
	"testing"
	"unsafe"
)

// The kernel's ioctl numbers on 64-bit targets, from <linux/videodev2.h>.
func TestIoctlNumbers(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the expected numbers are those of 64-bit targets")
	}
	tests := []struct {
		name      string
		got, want uintptr
	}{
		{"QUERYCAP", vidiocQueryCap, 0x80685600},
		{"ENUM_FMT", vidiocEnumFmt, 0xc0405602},
		{"S_FMT", vidiocSFmt, 0xc0d05605},
		{"REQBUFS", vidiocReqBufs, 0xc0145608},
		{"QUERYBUF", vidiocQueryBuf, 0xc0585609},
		{"QBUF", vidiocQBuf, 0xc058560f},
		{"DQBUF", vidiocDQBuf, 0xc0585611},
		{"STREAMON", vidiocStreamOn, 0x40045612},
		{"STREAMOFF", vidiocStreamOff, 0x40045613},
		{"S_PARM", vidiocSParm, 0xc0cc5616},
		{"ENUM_FRAMESIZES", vidiocEnumFrameSizes, 0xc02c564a},
		{"ENUM_FRAMEINTERVALS", vidiocEnumFrameIvals, 0xc034564b},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("VIDIOC_%s = %#x, want %#x", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestFourcc(t *testing.T) {
	if pixFmtYUYV != 0x56595559 || pixFmtMJPG != 0x47504a4d {
		t.Errorf("YUYV = %#x, MJPG = %#x", pixFmtYUYV, pixFmtMJPG)
	}
}
