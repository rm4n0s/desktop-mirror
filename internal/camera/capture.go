package camera

import (
	"context"
	"sync"

	"github.com/rm4n0s/desktop-mirror/internal/media"
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
