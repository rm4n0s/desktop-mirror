package camera

import (
	"context"
	"sync"
	"unsafe"

	"github.com/rm4n0s/errors"
	"golang.org/x/sys/unix"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

const (
	bufferCount = 4
	// pollMillis bounds how long Stop waits for the capture goroutine, which
	// checks for cancellation between polls.
	pollMillis = 100
	// maxBadFrames is how many frames in a row may fail to decode before the
	// camera is reported as failed.
	maxBadFrames = 30
)

// Capture runs one camera at a time and delivers its frames.
type Capture struct {
	onFrame func(media.RGBAFrame)
	onError func(error)

	// op serializes Start and Stop, which wait for the camera to be released.
	op     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates an idle Capture.
func New() *Capture { return &Capture{} }

// OnFrame sets the frame handler. It runs on the capture goroutine for every
// frame, so it must return quickly; the frame is the handler's to keep. Set it
// before Start.
func (c *Capture) OnFrame(fn func(media.RGBAFrame)) { c.onFrame = fn }

// OnError sets the handler for camera failures that happen after Start (the
// camera was unplugged, the driver failed). The camera is already released when
// it runs, and it may call Start or Stop. Set it before Start.
func (c *Capture) OnError(fn func(error)) { c.onError = fn }

// Start switches to the camera with the given id (a Device.ID), stopping the
// current one. The camera is opened and configured before Start returns, so a
// camera that cannot be used is reported here; frames then arrive on their own
// goroutine until Stop or ctx ends.
func (c *Capture) Start(ctx context.Context, id string) error {
	c.op.Lock()
	defer c.op.Unlock()
	c.stop()
	s, err := openStream(id)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.cancel, c.done = cancel, done

	go func() {
		err := s.run(ctx, c.onFrame)
		s.close()
		close(done)
		if err != nil && ctx.Err() == nil && c.onError != nil {
			c.onError(err)
		}
	}()
	return nil
}

// Stop stops the current camera, if any, and waits until it is released.
func (c *Capture) Stop() {
	c.op.Lock()
	defer c.op.Unlock()
	c.stop()
}

func (c *Capture) stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
	c.cancel, c.done = nil, nil
}

// mode is a camera mode: a pixel format and a size.
type mode struct {
	pixelFormat uint32
	FormatInfo
}

// stream is an open, configured camera that is streaming.
type stream struct {
	fd      int
	id      string
	buffers [][]byte

	pixelFormat   uint32
	width, height int
	stride        int
}

func openStream(id string) (*stream, error) {
	fd, err := unix.Open(id, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOENT || err == unix.ENODEV || err == unix.ENXIO {
			return nil, errors.New("CameraNotFound", "the selected camera is no longer available", "id", id)
		}
		return nil, errors.NewErr("CameraOpenFailed", err).SetMetadata("id", id)
	}
	s := &stream{fd: fd, id: id}
	if err := s.configure(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// configure picks the mode, maps the buffers and starts streaming.
func (s *stream) configure() error {
	if err := s.setFormat(chooseMode(listModes(s.fd))); err != nil {
		return err
	}
	s.setFrameRate()
	if err := s.mapBuffers(); err != nil {
		return err
	}
	typ := int32(bufTypeVideoCapture)
	if err := ioctl(s.fd, vidiocStreamOn, unsafe.Pointer(&typ)); err != nil {
		return errors.NewErr("CameraStreamOnFailed", err).SetMetadata("id", s.id)
	}
	return nil
}

// chooseMode returns the mode to ask for. A driver that cannot list its modes
// gets a request for the target size, which it adjusts to what it can do.
func chooseMode(modes []mode) mode {
	infos := make([]FormatInfo, len(modes))
	for i, m := range modes {
		infos[i] = m.FormatInfo
	}
	if best := BestFormat(infos); best >= 0 {
		return modes[best]
	}
	return mode{pixelFormat: pixFmtMJPG, FormatInfo: FormatInfo{Width: TargetWidth, Height: TargetHeight}}
}

// listModes lists the modes of the pixel formats that can be converted. Raw
// modes come first so that they win a tie: they need no decoding.
func listModes(fd int) []mode {
	supported := map[uint32]bool{}
	for i := uint32(0); ; i++ {
		desc := v4l2FmtDesc{Index: i, Type: bufTypeVideoCapture}
		if ioctl(fd, vidiocEnumFmt, unsafe.Pointer(&desc)) != nil {
			break
		}
		supported[desc.PixelFormat] = true
	}
	var modes []mode
	for _, pf := range []uint32{pixFmtYUYV, pixFmtMJPG} {
		if !supported[pf] {
			continue
		}
		for i := uint32(0); ; i++ {
			size := v4l2FrmSizeEnum{Index: i, PixelFormat: pf}
			if ioctl(fd, vidiocEnumFrameSizes, unsafe.Pointer(&size)) != nil {
				break
			}
			w, h := int(size.Width), int(size.Height)
			if size.Type != frmSizeDiscrete {
				// A range of sizes: ask for the target size, which the driver
				// rounds to what it can do. Its frame rate is not listed.
				modes = append(modes, mode{pf, FormatInfo{Width: TargetWidth, Height: TargetHeight, MaxFPS: targetFPS}})
				break
			}
			modes = append(modes, mode{pf, FormatInfo{Width: w, Height: h, MaxFPS: maxFrameRate(fd, pf, w, h)}})
		}
	}
	return modes
}

// maxFrameRate returns the highest frame rate of a mode; targetFPS when the
// driver does not list the rates.
func maxFrameRate(fd int, pixelFormat uint32, w, h int) float64 {
	best := 0.0
	for i := uint32(0); ; i++ {
		iv := v4l2FrmIvalEnum{Index: i, PixelFormat: pixelFormat, Width: uint32(w), Height: uint32(h)}
		if ioctl(fd, vidiocEnumFrameIvals, unsafe.Pointer(&iv)) != nil {
			break
		}
		if iv.Type != frmIvalDiscrete || iv.Interval.Numerator == 0 {
			return targetFPS
		}
		if fps := float64(iv.Interval.Denominator) / float64(iv.Interval.Numerator); fps > best {
			best = fps
		}
	}
	if best == 0 {
		return targetFPS
	}
	return best
}

// setFormat asks for m and keeps what the driver agrees to.
func (s *stream) setFormat(m mode) error {
	f := v4l2Format{Type: bufTypeVideoCapture}
	f.Pix = v4l2PixFormat{Width: uint32(m.Width), Height: uint32(m.Height), PixelFormat: m.pixelFormat}
	if err := ioctl(s.fd, vidiocSFmt, unsafe.Pointer(&f)); err != nil {
		return errors.NewErr("CameraSetFormatFailed", err).SetMetadata("id", s.id)
	}
	if f.Pix.PixelFormat != pixFmtYUYV && f.Pix.PixelFormat != pixFmtMJPG {
		return errors.New("CameraFormatUnsupported", "the camera offers neither YUYV nor MJPEG video",
			"id", s.id, "pixelFormat", f.Pix.PixelFormat)
	}
	s.pixelFormat = f.Pix.PixelFormat
	s.width, s.height = int(f.Pix.Width), int(f.Pix.Height)
	s.stride = int(f.Pix.BytesPerLine)
	if s.stride == 0 {
		s.stride = s.width * 2
	}
	return nil
}

// setFrameRate asks for the target frame rate. Drivers that cannot be told keep
// their own, so a failure is not one.
func (s *stream) setFrameRate() {
	p := v4l2StreamParm{Type: bufTypeVideoCapture}
	p.Capture.TimePerFrame = v4l2Fract{Numerator: 1, Denominator: targetFPS}
	_ = ioctl(s.fd, vidiocSParm, unsafe.Pointer(&p))
}

// mapBuffers asks the driver for buffers, maps them and queues them all.
func (s *stream) mapBuffers() error {
	req := v4l2RequestBuffers{Count: bufferCount, Type: bufTypeVideoCapture, Memory: memoryMmap}
	if err := ioctl(s.fd, vidiocReqBufs, unsafe.Pointer(&req)); err != nil {
		return errors.NewErr("CameraRequestBuffersFailed", err).SetMetadata("id", s.id)
	}
	if req.Count < 2 {
		return errors.New("CameraTooFewBuffers", "the camera driver gave too few buffers", "id", s.id, "count", req.Count)
	}
	for i := uint32(0); i < req.Count; i++ {
		buf := v4l2Buffer{Index: i, Type: bufTypeVideoCapture, Memory: memoryMmap}
		if err := ioctl(s.fd, vidiocQueryBuf, unsafe.Pointer(&buf)); err != nil {
			return errors.NewErr("CameraQueryBufferFailed", err).SetMetadata("id", s.id)
		}
		mem, err := unix.Mmap(s.fd, int64(uint32(buf.Offset)), int(buf.Length), unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			return errors.NewErr("CameraMapBufferFailed", err).SetMetadata("id", s.id)
		}
		s.buffers = append(s.buffers, mem)
		if err := ioctl(s.fd, vidiocQBuf, unsafe.Pointer(&buf)); err != nil {
			return errors.NewErr("CameraQueueBufferFailed", err).SetMetadata("id", s.id)
		}
	}
	return nil
}

// run delivers frames until ctx ends (nil) or the camera fails (the error).
func (s *stream) run(ctx context.Context, onFrame func(media.RGBAFrame)) error {
	fds := []unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLIN}}
	bad := 0
	for ctx.Err() == nil {
		n, err := unix.Poll(fds, pollMillis)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return errors.NewErr("CameraPollFailed", err).SetMetadata("id", s.id)
		}
		if n == 0 {
			continue
		}
		buf := v4l2Buffer{Type: bufTypeVideoCapture, Memory: memoryMmap}
		if err := ioctl(s.fd, vidiocDQBuf, unsafe.Pointer(&buf)); err != nil {
			if err == unix.EAGAIN {
				continue
			}
			if err == unix.ENODEV {
				return errors.New("CameraDisconnected", "the camera was unplugged", "id", s.id)
			}
			return errors.NewErr("CameraReadFailed", err).SetMetadata("id", s.id)
		}
		if int(buf.Index) >= len(s.buffers) {
			return errors.New("CameraBadBufferIndex", "the camera driver returned an unknown buffer",
				"id", s.id, "index", buf.Index)
		}

		frame, err := s.decode(buf)
		if err != nil {
			bad++
			if bad >= maxBadFrames {
				return err
			}
		} else {
			bad = 0
			if onFrame != nil {
				onFrame(frame)
			}
		}
		if err := ioctl(s.fd, vidiocQBuf, unsafe.Pointer(&buf)); err != nil {
			return errors.NewErr("CameraQueueBufferFailed", err).SetMetadata("id", s.id)
		}
	}
	return nil
}

// decode converts the frame in buf to RGBA. The result owns its memory, which
// the driver then gets back.
func (s *stream) decode(buf v4l2Buffer) (media.RGBAFrame, error) {
	if buf.Flags&bufFlagError != 0 {
		return media.RGBAFrame{}, errors.New("CameraCorruptFrame", "the camera delivered a corrupt frame", "id", s.id)
	}
	mem := s.buffers[buf.Index]
	data := mem[:min(int(buf.BytesUsed), len(mem))]
	if s.pixelFormat == pixFmtMJPG {
		return MJPEGToRGBA(data)
	}
	return YUYVToRGBA(data, s.width, s.height, s.stride)
}

// close releases the camera. It is safe after a failed configure.
func (s *stream) close() {
	typ := int32(bufTypeVideoCapture)
	_ = ioctl(s.fd, vidiocStreamOff, unsafe.Pointer(&typ)) // fails if streaming never started
	for _, mem := range s.buffers {
		_ = unix.Munmap(mem) // the mapping goes away with the process anyway
	}
	s.buffers = nil
	_ = unix.Close(s.fd)
}
