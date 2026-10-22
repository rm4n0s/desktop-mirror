package camera

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// The structs below mirror <linux/videodev2.h>. Go lays them out with the same
// natural alignment as C, and the ioctl numbers are derived from their sizes,
// so they stay right on 32-bit and 64-bit targets alike.

const (
	bufTypeVideoCapture = 1
	memoryMmap          = 1

	capVideoCapture = 0x00000001
	capStreaming    = 0x04000000
	capDeviceCaps   = 0x80000000

	bufFlagError = 0x00000040

	frmSizeDiscrete = 1
	frmIvalDiscrete = 1
)

// Pixel formats the capture understands.
var (
	pixFmtYUYV = fourcc('Y', 'U', 'Y', 'V')
	pixFmtMJPG = fourcc('M', 'J', 'P', 'G')
)

func fourcc(a, b, c, d byte) uint32 {
	return uint32(a) | uint32(b)<<8 | uint32(c)<<16 | uint32(d)<<24
}

type v4l2Capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

type v4l2FmtDesc struct {
	Index       uint32
	Type        uint32
	Flags       uint32
	Description [32]byte
	PixelFormat uint32
	MbusCode    uint32
	Reserved    [3]uint32
}

type v4l2PixFormat struct {
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	YcbcrEnc     uint32
	Quantization uint32
	XferFunc     uint32
}

// v4l2Format has a union that contains pointers in C, which aligns it to a
// pointer; that is what the first padding stands for.
type v4l2Format struct {
	Type uint32
	_    [unsafe.Sizeof(uintptr(0)) - 4]byte
	Pix  v4l2PixFormat
	_    [200 - unsafe.Sizeof(v4l2PixFormat{})]byte
}

type v4l2RequestBuffers struct {
	Count        uint32
	Type         uint32
	Memory       uint32
	Capabilities uint32
	Flags        uint8
	Reserved     [3]uint8
}

type v4l2Timecode struct {
	Type     uint32
	Flags    uint32
	Frames   uint8
	Seconds  uint8
	Minutes  uint8
	Hours    uint8
	Userbits [4]uint8
}

type v4l2Buffer struct {
	Index     uint32
	Type      uint32
	BytesUsed uint32
	Flags     uint32
	Field     uint32
	Timestamp unix.Timeval
	Timecode  v4l2Timecode
	Sequence  uint32
	Memory    uint32
	Offset    uintptr // the union m; for mmap buffers the offset is in its low 32 bits
	Length    uint32
	Reserved2 uint32
	RequestFD int32
}

type v4l2Fract struct {
	Numerator   uint32
	Denominator uint32
}

type v4l2CaptureParm struct {
	Capability   uint32
	CaptureMode  uint32
	TimePerFrame v4l2Fract
	ExtendedMode uint32
	ReadBuffers  uint32
	Reserved     [4]uint32
}

type v4l2StreamParm struct {
	Type    uint32
	Capture v4l2CaptureParm
	_       [200 - unsafe.Sizeof(v4l2CaptureParm{})]byte
}

type v4l2FrmSizeEnum struct {
	Index       uint32
	PixelFormat uint32
	Type        uint32
	Width       uint32 // the discrete size, or the minimum width of a range
	Height      uint32 // the discrete size, or the maximum width of a range
	Stepwise    [4]uint32
	Reserved    [2]uint32
}

type v4l2FrmIvalEnum struct {
	Index       uint32
	PixelFormat uint32
	Width       uint32
	Height      uint32
	Type        uint32
	Interval    v4l2Fract // the discrete interval, or the minimum of a range
	Stepwise    [4]uint32
	Reserved    [2]uint32
}

const (
	iocWrite = 1
	iocRead  = 2
)

// ioc is the _IOC macro of the kernel.
func ioc(dir, nr, size uintptr) uintptr {
	const typ = 'V'
	return dir<<30 | size<<16 | typ<<8 | nr
}

var (
	vidiocQueryCap       = ioc(iocRead, 0, unsafe.Sizeof(v4l2Capability{}))
	vidiocEnumFmt        = ioc(iocRead|iocWrite, 2, unsafe.Sizeof(v4l2FmtDesc{}))
	vidiocSFmt           = ioc(iocRead|iocWrite, 5, unsafe.Sizeof(v4l2Format{}))
	vidiocReqBufs        = ioc(iocRead|iocWrite, 8, unsafe.Sizeof(v4l2RequestBuffers{}))
	vidiocQueryBuf       = ioc(iocRead|iocWrite, 9, unsafe.Sizeof(v4l2Buffer{}))
	vidiocQBuf           = ioc(iocRead|iocWrite, 15, unsafe.Sizeof(v4l2Buffer{}))
	vidiocDQBuf          = ioc(iocRead|iocWrite, 17, unsafe.Sizeof(v4l2Buffer{}))
	vidiocStreamOn       = ioc(iocWrite, 18, unsafe.Sizeof(int32(0)))
	vidiocStreamOff      = ioc(iocWrite, 19, unsafe.Sizeof(int32(0)))
	vidiocSParm          = ioc(iocRead|iocWrite, 22, unsafe.Sizeof(v4l2StreamParm{}))
	vidiocEnumFrameSizes = ioc(iocRead|iocWrite, 74, unsafe.Sizeof(v4l2FrmSizeEnum{}))
	vidiocEnumFrameIvals = ioc(iocRead|iocWrite, 75, unsafe.Sizeof(v4l2FrmIvalEnum{}))
)

// ioctl issues request on fd with arg, which must point to the struct the
// request expects. It retries when a signal interrupts the call.
func ioctl(fd int, request uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(arg))
		switch errno {
		case 0:
			return nil
		case unix.EINTR:
			continue
		default:
			return errno
		}
	}
}

// cString returns the text of a NUL-terminated byte array.
func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
