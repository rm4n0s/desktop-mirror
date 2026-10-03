// Package ffmpeg implements media.Encoder and media.Decoder for VP8 on top of
// libavcodec (cgo). Encoding needs an FFmpeg build with libvpx.
package ffmpeg

/*
#cgo pkg-config: libavcodec libavutil
#include <stdlib.h>
#include <string.h>
#include <libavcodec/avcodec.h>
#include <libavutil/opt.h>
#include <libavutil/imgutils.h>
#include <libavutil/error.h>

typedef struct {
	AVCodecContext *ctx;
	AVFrame *frame;
	AVPacket *pkt;
} dm_codec;

static void dm_free(dm_codec *c) {
	if (!c) return;
	av_packet_free(&c->pkt);
	av_frame_free(&c->frame);
	avcodec_free_context(&c->ctx);
	free(c);
}

static const char *dm_errstr(int err, char *buf, size_t n) {
	if (av_strerror(err, buf, n) < 0) snprintf(buf, n, "ffmpeg error %d", err);
	return buf;
}

// dm_encoder_open returns NULL and sets *err (0 = encoder not found) on failure.
static dm_codec *dm_encoder_open(int w, int h, int bitrate, int fps, int threads, int *err) {
	const AVCodec *codec = avcodec_find_encoder_by_name("libvpx");
	if (!codec) { *err = 0; return NULL; }
	dm_codec *c = calloc(1, sizeof(dm_codec));
	c->ctx = avcodec_alloc_context3(codec);
	c->frame = av_frame_alloc();
	c->pkt = av_packet_alloc();
	if (!c->ctx || !c->frame || !c->pkt) { *err = AVERROR(ENOMEM); dm_free(c); return NULL; }
	c->ctx->width = w;
	c->ctx->height = h;
	c->ctx->pix_fmt = AV_PIX_FMT_YUV420P;
	c->ctx->time_base = (AVRational){1, fps};
	c->ctx->framerate = (AVRational){fps, 1};
	c->ctx->bit_rate = bitrate;
	c->ctx->gop_size = 100000; // keyframes only on request
	c->ctx->max_b_frames = 0;
	c->ctx->thread_count = threads;
	av_opt_set(c->ctx->priv_data, "deadline", "realtime", 0);
	av_opt_set(c->ctx->priv_data, "cpu-used", "6", 0);
	av_opt_set(c->ctx->priv_data, "lag-in-frames", "0", 0);
	av_opt_set(c->ctx->priv_data, "error-resilient", "default", 0);
	av_opt_set(c->ctx->priv_data, "auto-alt-ref", "0", 0);
	int r = avcodec_open2(c->ctx, codec, NULL);
	if (r < 0) { *err = r; dm_free(c); return NULL; }
	c->frame->format = AV_PIX_FMT_YUV420P;
	c->frame->width = w;
	c->frame->height = h;
	r = av_frame_get_buffer(c->frame, 0);
	if (r < 0) { *err = r; dm_free(c); return NULL; }
	return c;
}

// dm_encode copies a packed I420 frame in, encodes it and returns a malloc'd
// packet in *out (size 0 when the encoder produced nothing yet).
static int dm_encode(dm_codec *c, const uint8_t *data, int w, int h, int force_key, int64_t pts, uint8_t **out, int *out_size) {
	*out = NULL;
	*out_size = 0;
	int r = av_frame_make_writable(c->frame);
	if (r < 0) return r;
	const uint8_t *y = data, *u = y + w * h, *v = u + (w / 2) * (h / 2);
	av_image_copy_plane(c->frame->data[0], c->frame->linesize[0], y, w, w, h);
	av_image_copy_plane(c->frame->data[1], c->frame->linesize[1], u, w / 2, w / 2, h / 2);
	av_image_copy_plane(c->frame->data[2], c->frame->linesize[2], v, w / 2, w / 2, h / 2);
	c->frame->pts = pts;
	c->frame->pict_type = force_key ? AV_PICTURE_TYPE_I : AV_PICTURE_TYPE_NONE;
	r = avcodec_send_frame(c->ctx, c->frame);
	if (r < 0) return r;
	r = avcodec_receive_packet(c->ctx, c->pkt);
	if (r == AVERROR(EAGAIN)) return 0;
	if (r < 0) return r;
	*out = malloc(c->pkt->size);
	if (!*out) { av_packet_unref(c->pkt); return AVERROR(ENOMEM); }
	memcpy(*out, c->pkt->data, c->pkt->size);
	*out_size = c->pkt->size;
	av_packet_unref(c->pkt);
	return 0;
}

static dm_codec *dm_decoder_open(int *err) {
	const AVCodec *codec = avcodec_find_decoder(AV_CODEC_ID_VP8);
	if (!codec) { *err = 0; return NULL; }
	dm_codec *c = calloc(1, sizeof(dm_codec));
	c->ctx = avcodec_alloc_context3(codec);
	c->frame = av_frame_alloc();
	c->pkt = av_packet_alloc();
	if (!c->ctx || !c->frame || !c->pkt) { *err = AVERROR(ENOMEM); dm_free(c); return NULL; }
	c->ctx->thread_count = 1;
	c->ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;
	int r = avcodec_open2(c->ctx, codec, NULL);
	if (r < 0) { *err = r; dm_free(c); return NULL; }
	return c;
}

// dm_decode returns 1 when a picture is ready (see dm_frame_*), 0 when more
// data is needed, or a negative AVERROR.
static int dm_decode(dm_codec *c, const uint8_t *data, int size) {
	int r = av_new_packet(c->pkt, size);
	if (r < 0) return r;
	memcpy(c->pkt->data, data, size);
	r = avcodec_send_packet(c->ctx, c->pkt);
	av_packet_unref(c->pkt);
	if (r < 0) return r;
	av_frame_unref(c->frame);
	r = avcodec_receive_frame(c->ctx, c->frame);
	if (r == AVERROR(EAGAIN)) return 0;
	if (r < 0) return r;
	if (c->frame->format != AV_PIX_FMT_YUV420P && c->frame->format != AV_PIX_FMT_YUVJ420P) return AVERROR_INVALIDDATA;
	return 1;
}

static int dm_frame_width(dm_codec *c) { return c->frame->width; }
static int dm_frame_height(dm_codec *c) { return c->frame->height; }

// dm_frame_copy writes the decoded picture as packed I420 into dst.
static void dm_frame_copy(dm_codec *c, uint8_t *dst) {
	int w = c->frame->width, h = c->frame->height;
	uint8_t *y = dst, *u = y + w * h, *v = u + (w / 2) * (h / 2);
	av_image_copy_plane(y, w, c->frame->data[0], c->frame->linesize[0], w, h);
	av_image_copy_plane(u, w / 2, c->frame->data[1], c->frame->linesize[1], w / 2, h / 2);
	av_image_copy_plane(v, w / 2, c->frame->data[2], c->frame->linesize[2], w / 2, h / 2);
}
*/
import "C"

import (
	"context"
	"runtime"
	"sync"
	"unsafe"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

const encodeFrameRate = 30

func avError(code C.int) string {
	var buf [128]C.char
	return C.GoString(C.dm_errstr(code, &buf[0], C.size_t(len(buf))))
}

// Encoder is a libvpx VP8 encoder. It is safe for concurrent use.
type Encoder struct {
	mu            sync.Mutex
	c             *C.dm_codec
	width, height int
	bitrate       int
	pendingRate   int
	pts           int64
}

var _ media.Encoder = (*Encoder)(nil)

// NewEncoder opens a VP8 encoder. It matches media.EncoderFactory.
func NewEncoder(width, height, bitrate int) (media.Encoder, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, errors.New("InvalidFrameSize", "frame dimensions must be positive and even",
			"width", width, "height", height)
	}
	e := &Encoder{width: width, height: height, bitrate: bitrate}
	if err := e.open(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Encoder) open() error {
	var cerr C.int
	threads := min(runtime.NumCPU(), 4)
	c := C.dm_encoder_open(C.int(e.width), C.int(e.height), C.int(e.bitrate), encodeFrameRate, C.int(threads), &cerr)
	if c == nil {
		if cerr == 0 {
			return errors.New("EncoderUnavailable", "FFmpeg was built without the libvpx VP8 encoder")
		}
		return errors.New("EncoderOpenFailed", avError(cerr), "width", e.width, "height", e.height)
	}
	e.c = c
	return nil
}

// Encode implements media.Encoder.
func (e *Encoder) Encode(ctx context.Context, f media.I420Frame, forceKeyframe bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.NewErr("EncodeFailed", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.c == nil {
		return nil, errors.New("EncodeFailed", "encoder is closed")
	}
	if f.Width != e.width || f.Height != e.height || !f.Valid() {
		return nil, errors.New("EncodeFailed", "frame does not match the encoder size",
			"frameWidth", f.Width, "frameHeight", f.Height, "encoderWidth", e.width, "encoderHeight", e.height)
	}
	if e.pendingRate != 0 && e.pendingRate != e.bitrate {
		// libavcodec's libvpx wrapper cannot change the rate on the fly.
		C.dm_free(e.c)
		e.c, e.bitrate, e.pendingRate = nil, e.pendingRate, 0
		if err := e.open(); err != nil {
			return nil, err
		}
		forceKeyframe = true
	}
	force := C.int(0)
	if forceKeyframe {
		force = 1
	}
	var out *C.uint8_t
	var outSize C.int
	r := C.dm_encode(e.c, (*C.uint8_t)(unsafe.Pointer(&f.Data[0])), C.int(f.Width), C.int(f.Height), force, C.int64_t(e.pts), &out, &outSize)
	e.pts++
	if r < 0 {
		return nil, errors.New("EncodeFailed", avError(r))
	}
	if outSize == 0 {
		return nil, nil
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoBytes(unsafe.Pointer(out), outSize), nil
}

// SetBitrate schedules a bitrate change; the encoder restarts (with a
// keyframe) on the next Encode.
func (e *Encoder) SetBitrate(bps int) error {
	if bps <= 0 {
		return errors.New("InvalidBitrate", "bitrate must be positive", "bps", bps)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingRate = bps
	return nil
}

// Close implements media.Encoder.
func (e *Encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	C.dm_free(e.c)
	e.c = nil
	return nil
}

// Decoder is a VP8 decoder. It is safe for concurrent use.
type Decoder struct {
	mu sync.Mutex
	c  *C.dm_codec
}

var _ media.Decoder = (*Decoder)(nil)

// NewDecoder opens a VP8 decoder. It matches media.DecoderFactory.
func NewDecoder() (media.Decoder, error) {
	var cerr C.int
	c := C.dm_decoder_open(&cerr)
	if c == nil {
		if cerr == 0 {
			return nil, errors.New("DecoderUnavailable", "FFmpeg has no VP8 decoder")
		}
		return nil, errors.New("DecoderOpenFailed", avError(cerr))
	}
	return &Decoder{c: c}, nil
}

// Decode implements media.Decoder.
func (d *Decoder) Decode(ctx context.Context, au []byte) (media.I420Frame, bool, error) {
	if err := ctx.Err(); err != nil {
		return media.I420Frame{}, false, errors.NewErr("DecodeFailed", err)
	}
	if len(au) == 0 {
		return media.I420Frame{}, false, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.c == nil {
		return media.I420Frame{}, false, errors.New("DecodeFailed", "decoder is closed")
	}
	r := C.dm_decode(d.c, (*C.uint8_t)(unsafe.Pointer(&au[0])), C.int(len(au)))
	if r < 0 {
		return media.I420Frame{}, false, errors.New("DecodeFailed", avError(r))
	}
	if r == 0 {
		return media.I420Frame{}, false, nil
	}
	w, h := int(C.dm_frame_width(d.c)), int(C.dm_frame_height(d.c))
	f, err := media.NewI420(w, h)
	if err != nil {
		return media.I420Frame{}, false, errors.New("DecodeFailed", "decoder produced an unsupported frame size",
			"width", w, "height", h)
	}
	C.dm_frame_copy(d.c, (*C.uint8_t)(unsafe.Pointer(&f.Data[0])))
	return f, true, nil
}

// Close implements media.Decoder.
func (d *Decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	C.dm_free(d.c)
	d.c = nil
	return nil
}
