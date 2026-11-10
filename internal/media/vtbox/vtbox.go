//go:build darwin && cgo

// Package vtbox implements media.Encoder and media.Decoder for H.264 on top of VideoToolbox, the codec framework that every Mac
// has: nothing is installed for it, and the Mac's hardware encoder and decoder are used when it has them. Access units are Annex B
// byte streams (start codes), as pion's H.264 payloader and depacketizer use; a keyframe carries its parameter sets.
package vtbox

/*
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreVideo -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#include <VideoToolbox/VideoToolbox.h>
#include <CoreMedia/CoreMedia.h>
#include <CoreVideo/CoreVideo.h>

// Frames cross VideoToolbox as NV12 in video range ('420v'), its native format; the program's frames are I420 in the same range.
#define DM_PIXEL_FORMAT kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange

typedef struct {
	VTCompressionSessionRef session;
	int width, height, fps;
	int64_t pts;
	// The access unit of the frame that was just encoded, as Annex B; size 0 when the encoder gave none.
	uint8_t *out;
	size_t out_size, out_cap;
	OSStatus status;
} dm_enc;

static int dm_enc_append(dm_enc *e, const uint8_t *data, size_t n) {
	if (e->out_size + n > e->out_cap) {
		size_t cap = e->out_cap ? e->out_cap * 2 : 1 << 16;
		while (cap < e->out_size + n) cap *= 2;
		uint8_t *p = realloc(e->out, cap);
		if (!p) return 0;
		e->out = p;
		e->out_cap = cap;
	}
	memcpy(e->out + e->out_size, data, n);
	e->out_size += n;
	return 1;
}

static const uint8_t dm_start_code[4] = {0, 0, 0, 1};

// dm_enc_output is called by VideoToolbox with the encoded frame, before VTCompressionSessionCompleteFrames returns.
static void dm_enc_output(void *refcon, void *frame_refcon, OSStatus status, VTEncodeInfoFlags flags, CMSampleBufferRef sb) {
	dm_enc *e = refcon;
	e->status = status;
	if (status != noErr || !sb || !CMSampleBufferDataIsReady(sb)) return; // an error, or a frame that the encoder dropped
	int key = 1;
	CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
	if (atts && CFArrayGetCount(atts) > 0) {
		CFDictionaryRef d = CFArrayGetValueAtIndex(atts, 0);
		CFBooleanRef not_sync = CFDictionaryGetValue(d, kCMSampleAttachmentKey_NotSync);
		key = !(not_sync && CFBooleanGetValue(not_sync));
	}
	CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sb);
	size_t count = 0;
	int header = 4;
	OSStatus s = CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 0, NULL, NULL, &count, &header);
	if (s != noErr) { e->status = s; return; }
	if (key) {
		for (size_t i = 0; i < count; i++) {
			const uint8_t *ps = NULL;
			size_t n = 0;
			s = CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, i, &ps, &n, NULL, NULL);
			if (s != noErr) { e->status = s; return; }
			if (!dm_enc_append(e, dm_start_code, 4) || !dm_enc_append(e, ps, n)) { e->status = -108; return; }
		}
	}
	CMBlockBufferRef bb = CMSampleBufferGetDataBuffer(sb);
	size_t len = CMBlockBufferGetDataLength(bb);
	uint8_t *tmp = malloc(len ? len : 1);
	if (!tmp) { e->status = -108; return; }
	s = CMBlockBufferCopyDataBytes(bb, 0, len, tmp);
	if (s != noErr) { free(tmp); e->status = s; return; }
	// The data are NAL units after their lengths ("AVCC"); the lengths become start codes.
	size_t off = 0;
	while (off + header <= len) {
		size_t n = 0;
		for (int i = 0; i < header; i++) n = (n << 8) | tmp[off + i];
		off += header;
		if (n > len - off) { e->status = -12909; break; } // kVTVideoDecoderBadDataErr: lengths that do not fit
		if (!dm_enc_append(e, dm_start_code, 4) || !dm_enc_append(e, tmp + off, n)) { e->status = -108; break; }
		off += n;
	}
	free(tmp);
}

static void dm_enc_set_int(dm_enc *e, CFStringRef key, int v) {
	CFNumberRef n = CFNumberCreate(NULL, kCFNumberIntType, &v);
	VTSessionSetProperty(e->session, key, n);
	CFRelease(n);
}

static void dm_enc_free(dm_enc *e) {
	if (!e) return;
	if (e->session) {
		VTCompressionSessionInvalidate(e->session);
		CFRelease(e->session);
	}
	free(e->out);
	free(e);
}

static dm_enc *dm_enc_open(int w, int h, int bitrate, int fps, OSStatus *err) {
	dm_enc *e = calloc(1, sizeof(dm_enc));
	if (!e) { *err = -108; return NULL; }
	e->width = w;
	e->height = h;
	e->fps = fps;
	OSStatus s = VTCompressionSessionCreate(NULL, w, h, kCMVideoCodecType_H264, NULL, NULL, NULL, dm_enc_output, e, &e->session);
	if (s != noErr) { *err = s; dm_enc_free(e); return NULL; }
	// A live stream: no delay, no B-frames, the baseline profile that every browser decodes, keyframes on request only.
	VTSessionSetProperty(e->session, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
	VTSessionSetProperty(e->session, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
	VTSessionSetProperty(e->session, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_Baseline_AutoLevel);
	dm_enc_set_int(e, kVTCompressionPropertyKey_MaxKeyFrameInterval, 100000);
	dm_enc_set_int(e, kVTCompressionPropertyKey_ExpectedFrameRate, fps);
	dm_enc_set_int(e, kVTCompressionPropertyKey_AverageBitRate, bitrate);
	s = VTCompressionSessionPrepareToEncodeFrames(e->session);
	if (s != noErr) { *err = s; dm_enc_free(e); return NULL; }
	return e;
}

static void dm_enc_set_bitrate(dm_enc *e, int bitrate) {
	dm_enc_set_int(e, kVTCompressionPropertyKey_AverageBitRate, bitrate);
}

// dm_enc_encode encodes a packed I420 frame and leaves the access unit in e->out (e->out_size bytes, 0 when the encoder gave
// none).
static OSStatus dm_enc_encode(dm_enc *e, const uint8_t *data, int force_key) {
	int w = e->width, h = e->height;
	e->out_size = 0;
	e->status = noErr;
	CFDictionaryRef empty = CFDictionaryCreate(NULL, NULL, NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	const void *akeys[] = {kCVPixelBufferIOSurfacePropertiesKey};
	const void *avals[] = {empty};
	CFDictionaryRef attrs = CFDictionaryCreate(NULL, akeys, avals, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CVPixelBufferRef pb = NULL;
	CVReturn cr = CVPixelBufferCreate(NULL, w, h, DM_PIXEL_FORMAT, attrs, &pb);
	CFRelease(attrs);
	CFRelease(empty);
	if (cr != kCVReturnSuccess) return cr;
	cr = CVPixelBufferLockBaseAddress(pb, 0);
	if (cr != kCVReturnSuccess) { CVPixelBufferRelease(pb); return cr; }
	const uint8_t *y = data, *u = y + w * h, *v = u + (w / 2) * (h / 2);
	uint8_t *dst = CVPixelBufferGetBaseAddressOfPlane(pb, 0);
	size_t stride = CVPixelBufferGetBytesPerRowOfPlane(pb, 0);
	for (int row = 0; row < h; row++) memcpy(dst + row * stride, y + row * w, w);
	dst = CVPixelBufferGetBaseAddressOfPlane(pb, 1);
	stride = CVPixelBufferGetBytesPerRowOfPlane(pb, 1);
	for (int row = 0; row < h / 2; row++) {
		uint8_t *d = dst + row * stride;
		const uint8_t *ur = u + row * (w / 2), *vr = v + row * (w / 2);
		for (int x = 0; x < w / 2; x++) {
			d[2 * x] = ur[x];
			d[2 * x + 1] = vr[x];
		}
	}
	CVPixelBufferUnlockBaseAddress(pb, 0);

	CFDictionaryRef props = NULL;
	if (force_key) {
		const void *keys[] = {kVTEncodeFrameOptionKey_ForceKeyFrame};
		const void *vals[] = {kCFBooleanTrue};
		props = CFDictionaryCreate(NULL, keys, vals, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	}
	OSStatus s = VTCompressionSessionEncodeFrame(e->session, pb, CMTimeMake(e->pts++, e->fps), kCMTimeInvalid, props, NULL, NULL);
	if (props) CFRelease(props);
	CVPixelBufferRelease(pb);
	if (s != noErr) return s;
	s = VTCompressionSessionCompleteFrames(e->session, kCMTimeInvalid);
	if (s != noErr) return s;
	return e->status;
}

typedef struct {
	VTDecompressionSessionRef session;
	CMVideoFormatDescriptionRef fmt;
	// The picture of the access unit that was just decoded, as packed I420; have is 0 when the decoder gave none.
	uint8_t *out;
	size_t out_cap;
	int width, height, have;
	OSStatus status;
} dm_dec;

// dm_dec_output is called by VideoToolbox with the decoded picture, before VTDecompressionSessionDecodeFrame returns (the
// decoding is asked for synchronously).
static void dm_dec_output(void *refcon, void *frame_refcon, OSStatus status, VTDecodeInfoFlags flags, CVImageBufferRef image, CMTime pts, CMTime duration) {
	dm_dec *d = refcon;
	d->status = status;
	if (status != noErr || !image) return;
	if (CVPixelBufferGetPixelFormatType(image) != DM_PIXEL_FORMAT) { d->status = -12910; return; } // kVTVideoDecoderUnsupportedDataFormatErr
	if (CVPixelBufferLockBaseAddress(image, kCVPixelBufferLock_ReadOnly) != kCVReturnSuccess) { d->status = -12911; return; }
	// An odd size is cut by one pixel: the program's frames have even sizes.
	int w = (int)CVPixelBufferGetWidth(image) & ~1, h = (int)CVPixelBufferGetHeight(image) & ~1;
	size_t need = (size_t)w * h + 2 * (size_t)(w / 2) * (h / 2);
	if (need > d->out_cap) {
		uint8_t *p = realloc(d->out, need);
		if (!p) { CVPixelBufferUnlockBaseAddress(image, kCVPixelBufferLock_ReadOnly); d->status = -108; return; }
		d->out = p;
		d->out_cap = need;
	}
	const uint8_t *src = CVPixelBufferGetBaseAddressOfPlane(image, 0);
	size_t stride = CVPixelBufferGetBytesPerRowOfPlane(image, 0);
	uint8_t *y = d->out, *u = y + (size_t)w * h, *v = u + (size_t)(w / 2) * (h / 2);
	for (int row = 0; row < h; row++) memcpy(y + (size_t)row * w, src + row * stride, w);
	src = CVPixelBufferGetBaseAddressOfPlane(image, 1);
	stride = CVPixelBufferGetBytesPerRowOfPlane(image, 1);
	for (int row = 0; row < h / 2; row++) {
		const uint8_t *s = src + row * stride;
		uint8_t *ur = u + (size_t)row * (w / 2), *vr = v + (size_t)row * (w / 2);
		for (int x = 0; x < w / 2; x++) {
			ur[x] = s[2 * x];
			vr[x] = s[2 * x + 1];
		}
	}
	CVPixelBufferUnlockBaseAddress(image, kCVPixelBufferLock_ReadOnly);
	d->width = w;
	d->height = h;
	d->have = 1;
}

static void dm_dec_close_session(dm_dec *d) {
	if (d->session) {
		VTDecompressionSessionInvalidate(d->session);
		CFRelease(d->session);
		d->session = NULL;
	}
	if (d->fmt) {
		CFRelease(d->fmt);
		d->fmt = NULL;
	}
}

static void dm_dec_free(dm_dec *d) {
	if (!d) return;
	dm_dec_close_session(d);
	free(d->out);
	free(d);
}

static dm_dec *dm_dec_open(void) { return calloc(1, sizeof(dm_dec)); }

// dm_dec_configure gives the decoder the parameter sets of a keyframe: a session is made for them, unless the one that exists
// takes them.
static OSStatus dm_dec_configure(dm_dec *d, const uint8_t *sps, size_t sps_len, const uint8_t *pps, size_t pps_len) {
	const uint8_t *sets[2] = {sps, pps};
	size_t sizes[2] = {sps_len, pps_len};
	CMVideoFormatDescriptionRef fmt = NULL;
	OSStatus s = CMVideoFormatDescriptionCreateFromH264ParameterSets(NULL, 2, sets, sizes, 4, &fmt);
	if (s != noErr) return s;
	if (d->session && d->fmt && CMFormatDescriptionEqual(fmt, d->fmt)) {
		CFRelease(fmt);
		return noErr;
	}
	if (d->session && VTDecompressionSessionCanAcceptFormatDescription(d->session, fmt)) {
		CFRelease(d->fmt);
		d->fmt = fmt;
		return noErr;
	}
	dm_dec_close_session(d);
	int format = DM_PIXEL_FORMAT;
	CFNumberRef n = CFNumberCreate(NULL, kCFNumberIntType, &format);
	const void *keys[] = {kCVPixelBufferPixelFormatTypeKey};
	const void *vals[] = {n};
	CFDictionaryRef attrs = CFDictionaryCreate(NULL, keys, vals, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	VTDecompressionOutputCallbackRecord cb = {dm_dec_output, d};
	s = VTDecompressionSessionCreate(NULL, fmt, NULL, attrs, &cb, &d->session);
	CFRelease(attrs);
	CFRelease(n);
	if (s != noErr) { CFRelease(fmt); d->session = NULL; return s; }
	d->fmt = fmt;
	return noErr;
}

static int dm_dec_ready(dm_dec *d) { return d->session != NULL; }

// dm_dec_decode decodes the slices of one access unit, given as NAL units after their four-byte lengths. It returns noErr with
// d->have set when a picture came out.
static OSStatus dm_dec_decode(dm_dec *d, const uint8_t *avcc, size_t len) {
	d->have = 0;
	d->status = noErr;
	CMBlockBufferRef bb = NULL;
	OSStatus s = CMBlockBufferCreateWithMemoryBlock(NULL, (void *)avcc, len, kCFAllocatorNull, NULL, 0, len, 0, &bb);
	if (s != noErr) return s;
	CMSampleBufferRef sb = NULL;
	const size_t sizes[1] = {len};
	s = CMSampleBufferCreateReady(NULL, bb, d->fmt, 1, 0, NULL, 1, sizes, &sb);
	CFRelease(bb);
	if (s != noErr) return s;
	VTDecodeInfoFlags info = 0;
	s = VTDecompressionSessionDecodeFrame(d->session, sb, 0, NULL, &info);
	if (s == noErr) s = VTDecompressionSessionWaitForAsynchronousFrames(d->session);
	CFRelease(sb);
	if (s != noErr) return s;
	return d->status;
}
*/
import "C"

import (
	"context"
	"sync"
	"unsafe"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
	"github.com/rm4n0s/desktop-mirror/internal/media/h264"
)

// encodeFrameRate is the frame rate the encoder plans its bitrate for.
const encodeFrameRate = 30

// Encoder is an H.264 encoder of VideoToolbox. It is safe for concurrent use.
type Encoder struct {
	mu            sync.Mutex
	c             *C.dm_enc
	width, height int
}

var _ media.Encoder = (*Encoder)(nil)

// NewEncoder opens an H.264 encoder. It matches media.EncoderFactory. It fails with the tag InvalidFrameSize for a size that is
// not positive and even, InvalidBitrate for a bitrate that is not positive, and EncoderOpenFailed (with VideoToolbox's status)
// when the session cannot be made.
func NewEncoder(width, height, bitrate int) (media.Encoder, error) {
	if width <= 0 || height <= 0 || width%2 != 0 || height%2 != 0 {
		return nil, errors.New("InvalidFrameSize", "frame dimensions must be positive and even",
			"width", width, "height", height)
	}
	if bitrate <= 0 {
		return nil, errors.New("InvalidBitrate", "bitrate must be positive", "bps", bitrate)
	}
	var status C.OSStatus
	c := C.dm_enc_open(C.int(width), C.int(height), C.int(bitrate), encodeFrameRate, &status)
	if c == nil {
		return nil, errors.New("EncoderOpenFailed", "VideoToolbox could not make an H.264 encoder",
			"status", int(status), "width", width, "height", height)
	}
	return &Encoder{c: c, width: width, height: height}, nil
}

// Encode implements media.Encoder: the result is one Annex B access unit, with the parameter sets before a keyframe. It fails
// with the tag EncodeFailed.
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
	force := C.int(0)
	if forceKeyframe {
		force = 1
	}
	if status := C.dm_enc_encode(e.c, (*C.uint8_t)(unsafe.Pointer(&f.Data[0])), force); status != 0 {
		return nil, errors.New("EncodeFailed", "VideoToolbox could not encode the frame", "status", int(status))
	}
	if e.c.out_size == 0 {
		return nil, nil
	}
	return C.GoBytes(unsafe.Pointer(e.c.out), C.int(e.c.out_size)), nil
}

// SetBitrate changes the average bitrate of the stream from the next frame on. It fails with the tag InvalidBitrate.
func (e *Encoder) SetBitrate(bps int) error {
	if bps <= 0 {
		return errors.New("InvalidBitrate", "bitrate must be positive", "bps", bps)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.c != nil {
		C.dm_enc_set_bitrate(e.c, C.int(bps))
	}
	return nil
}

// Close implements media.Encoder.
func (e *Encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	C.dm_enc_free(e.c)
	e.c = nil
	return nil
}

// Decoder is an H.264 decoder of VideoToolbox. It is safe for concurrent use.
type Decoder struct {
	mu sync.Mutex
	c  *C.dm_dec
}

var _ media.Decoder = (*Decoder)(nil)

// NewDecoder opens an H.264 decoder. It matches media.DecoderFactory. The decoding session itself is made when the first
// keyframe brings the parameter sets of the stream.
func NewDecoder() (media.Decoder, error) {
	c := C.dm_dec_open()
	if c == nil {
		return nil, errors.New("DecoderOpenFailed", "out of memory")
	}
	return &Decoder{c: c}, nil
}

// Decode implements media.Decoder for one Annex B access unit. Until a keyframe with its parameter sets has come, an access unit
// gives no picture (ok is false). It fails with the tag DecodeFailed, after which the stream needs a keyframe.
func (d *Decoder) Decode(ctx context.Context, au []byte) (media.I420Frame, bool, error) {
	if err := ctx.Err(); err != nil {
		return media.I420Frame{}, false, errors.NewErr("DecodeFailed", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.c == nil {
		return media.I420Frame{}, false, errors.New("DecodeFailed", "decoder is closed")
	}
	var sps, pps []byte
	var avcc []byte
	for _, nal := range h264.SplitAnnexB(au) {
		switch h264.Type(nal) {
		case h264.NALSPS:
			sps = nal
		case h264.NALPPS:
			pps = nal
		case h264.NALSlice, h264.NALIDR:
			avcc = h264.AppendAVCC(avcc, nal)
		}
	}
	if sps != nil && pps != nil {
		status := C.dm_dec_configure(d.c, (*C.uint8_t)(unsafe.Pointer(&sps[0])), C.size_t(len(sps)), (*C.uint8_t)(unsafe.Pointer(&pps[0])), C.size_t(len(pps)))
		if status != 0 {
			return media.I420Frame{}, false, errors.New("DecodeFailed", "VideoToolbox does not take the parameter sets of the stream", "status", int(status))
		}
	}
	if len(avcc) == 0 || C.dm_dec_ready(d.c) == 0 {
		return media.I420Frame{}, false, nil
	}
	if status := C.dm_dec_decode(d.c, (*C.uint8_t)(unsafe.Pointer(&avcc[0])), C.size_t(len(avcc))); status != 0 {
		return media.I420Frame{}, false, errors.New("DecodeFailed", "VideoToolbox could not decode the access unit", "status", int(status))
	}
	if d.c.have == 0 {
		return media.I420Frame{}, false, nil
	}
	w, h := int(d.c.width), int(d.c.height)
	f, err := media.NewI420(w, h)
	if err != nil {
		return media.I420Frame{}, false, errors.New("DecodeFailed", "decoder produced an unsupported frame size",
			"width", w, "height", h)
	}
	copy(f.Data, unsafe.Slice((*byte)(unsafe.Pointer(d.c.out)), len(f.Data)))
	return f, true, nil
}

// Close implements media.Decoder.
func (d *Decoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	C.dm_dec_free(d.c)
	d.c = nil
	return nil
}
