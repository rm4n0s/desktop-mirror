//go:build darwin && cgo

package camera

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation
#cgo LDFLAGS: -Wl,-sectcreate,__TEXT,__info_plist,${SRCDIR}/Info.plist
#import <AVFoundation/AVFoundation.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <sys/time.h>

// DMCamera runs one capture session. AVFoundation hands it the frames on a queue of its own; it keeps the newest one as packed
// RGBA, and the Go side takes it with dm_cam_wait.
@interface DMCamera : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate> {
@public
	pthread_mutex_t mu;
	pthread_cond_t cond;
	uint8_t *pending, *taken; // the newest frame, and the one that the Go side holds
	size_t pendingCap, takenCap;
	int width, height, fresh;
}
@property(nonatomic, strong) AVCaptureSession *session;
@property(nonatomic, strong) AVCaptureDevice *device;
@property(nonatomic, strong) dispatch_queue_t queue;
@end

@implementation DMCamera
- (instancetype)init {
	if ((self = [super init])) {
		pthread_mutex_init(&mu, NULL);
		pthread_cond_init(&cond, NULL);
	}
	return self;
}

- (void)dealloc {
	free(pending);
	free(taken);
	pthread_cond_destroy(&cond);
	pthread_mutex_destroy(&mu);
}

- (void)captureOutput:(AVCaptureOutput *)output didOutputSampleBuffer:(CMSampleBufferRef)sb fromConnection:(AVCaptureConnection *)connection {
	CVImageBufferRef image = CMSampleBufferGetImageBuffer(sb);
	if (!image || CVPixelBufferGetPixelFormatType(image) != kCVPixelFormatType_32BGRA) return;
	if (CVPixelBufferLockBaseAddress(image, kCVPixelBufferLock_ReadOnly) != kCVReturnSuccess) return;
	int w = (int)CVPixelBufferGetWidth(image), h = (int)CVPixelBufferGetHeight(image);
	size_t stride = CVPixelBufferGetBytesPerRow(image);
	const uint8_t *src = CVPixelBufferGetBaseAddress(image);
	size_t need = (size_t)w * h * 4;
	pthread_mutex_lock(&mu);
	if (need > pendingCap) {
		uint8_t *p = realloc(pending, need);
		if (p) {
			pending = p;
			pendingCap = need;
		}
	}
	if (need <= pendingCap && src) {
		for (int row = 0; row < h; row++) {
			const uint8_t *s = src + row * stride;
			uint8_t *d = pending + (size_t)row * w * 4;
			for (int x = 0; x < w; x++) { // BGRA to RGBA
				d[4 * x] = s[4 * x + 2];
				d[4 * x + 1] = s[4 * x + 1];
				d[4 * x + 2] = s[4 * x];
				d[4 * x + 3] = 255;
			}
		}
		width = w;
		height = h;
		fresh = 1;
		pthread_cond_signal(&cond);
	}
	pthread_mutex_unlock(&mu);
	CVPixelBufferUnlockBaseAddress(image, kCVPixelBufferLock_ReadOnly);
}
@end

static NSArray<AVCaptureDevice *> *dm_cam_list(void) {
	NSMutableArray<AVCaptureDeviceType> *types = [NSMutableArray arrayWithObject:AVCaptureDeviceTypeBuiltInWideAngleCamera];
	if (@available(macOS 14.0, *)) {
		[types addObject:AVCaptureDeviceTypeExternal];
		[types addObject:AVCaptureDeviceTypeContinuityCamera];
	} else {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
		[types addObject:AVCaptureDeviceTypeExternalUnknown];
#pragma clang diagnostic pop
	}
	AVCaptureDeviceDiscoverySession *ds = [AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:types
	                                                                                              mediaType:AVMediaTypeVideo
	                                                                                               position:AVCaptureDevicePositionUnspecified];
	return ds.devices;
}

// dm_cam_devices returns the cameras as lines of "unique id<TAB>name", in a string that the caller frees.
static char *dm_cam_devices(void) {
	@autoreleasepool {
		NSMutableString *out = [NSMutableString string];
		for (AVCaptureDevice *d in dm_cam_list()) {
			NSString *name = [[d.localizedName stringByReplacingOccurrencesOfString:@"\t" withString:@" "] stringByReplacingOccurrencesOfString:@"\n" withString:@" "];
			[out appendFormat:@"%@\t%@\n", d.uniqueID, name];
		}
		return strdup(out.UTF8String);
	}
}

// dm_cam_authorize says whether the program may use the camera: 1 yes, 0 no. The first time, the system asks the user, and the
// call waits for the answer.
static int dm_cam_authorize(void) {
	@autoreleasepool {
		AVAuthorizationStatus st = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
		if (st == AVAuthorizationStatusAuthorized) return 1;
		if (st != AVAuthorizationStatusNotDetermined) return 0;
		dispatch_semaphore_t done = dispatch_semaphore_create(0);
		__block BOOL granted = NO;
		[AVCaptureDevice requestAccessForMediaType:AVMediaTypeVideo completionHandler:^(BOOL ok) {
			granted = ok;
			dispatch_semaphore_signal(done);
		}];
		dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
		return granted ? 1 : 0;
	}
}

enum { DM_CAM_OK = 0, DM_CAM_NOT_FOUND = 1, DM_CAM_INPUT_FAILED = 2, DM_CAM_SESSION_FAILED = 3 };

// dm_cam_open starts capturing from the camera with the unique id, at about 1280x720. On failure it returns NULL, sets *err to
// one of the DM_CAM values and, for DM_CAM_INPUT_FAILED, *reason to the system's words (which the caller frees).
static void *dm_cam_open(const char *unique_id, int *err, char **reason) {
	@autoreleasepool {
		*err = DM_CAM_OK;
		*reason = NULL;
		AVCaptureDevice *device = nil;
		NSString *wanted = [NSString stringWithUTF8String:unique_id];
		for (AVCaptureDevice *d in dm_cam_list()) {
			if ([d.uniqueID isEqualToString:wanted]) device = d;
		}
		if (!device) { *err = DM_CAM_NOT_FOUND; return NULL; }
		NSError *error = nil;
		AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:device error:&error];
		if (!input) {
			*err = DM_CAM_INPUT_FAILED;
			*reason = strdup((error.localizedDescription ?: @"unknown error").UTF8String);
			return NULL;
		}
		DMCamera *cam = [[DMCamera alloc] init];
		cam.device = device;
		cam.session = [[AVCaptureSession alloc] init];
		cam.queue = dispatch_queue_create("dev.rm4n0s.desktop-mirror.camera", DISPATCH_QUEUE_SERIAL);
		[cam.session beginConfiguration];
		if ([cam.session canSetSessionPreset:AVCaptureSessionPreset1280x720]) {
			cam.session.sessionPreset = AVCaptureSessionPreset1280x720;
		}
		AVCaptureVideoDataOutput *output = [[AVCaptureVideoDataOutput alloc] init];
		output.videoSettings = @{(id)kCVPixelBufferPixelFormatTypeKey : @(kCVPixelFormatType_32BGRA)};
		output.alwaysDiscardsLateVideoFrames = YES;
		[output setSampleBufferDelegate:cam queue:cam.queue];
		if (![cam.session canAddInput:input] || ![cam.session canAddOutput:output]) {
			[cam.session commitConfiguration];
			*err = DM_CAM_SESSION_FAILED;
			return NULL;
		}
		[cam.session addInput:input];
		[cam.session addOutput:output];
		[cam.session commitConfiguration];
		[cam.session startRunning];
		return (__bridge_retained void *)cam;
	}
}

// dm_cam_wait waits up to timeout_ms for a new frame. It returns 1 with the frame (packed RGBA, which stays valid until the next
// call), 0 when none came in time, and -1 when the camera is gone.
static int dm_cam_wait(void *handle, int timeout_ms, uint8_t **data, int *w, int *h) {
	DMCamera *cam = (__bridge DMCamera *)handle;
	struct timeval now;
	gettimeofday(&now, NULL);
	struct timespec until;
	until.tv_sec = now.tv_sec + timeout_ms / 1000;
	until.tv_nsec = (now.tv_usec + (timeout_ms % 1000) * 1000L) * 1000L;
	if (until.tv_nsec >= 1000000000L) {
		until.tv_sec++;
		until.tv_nsec -= 1000000000L;
	}
	int got = 0;
	pthread_mutex_lock(&cam->mu);
	while (!cam->fresh) {
		if (pthread_cond_timedwait(&cam->cond, &cam->mu, &until) != 0) break;
	}
	if (cam->fresh) {
		uint8_t *p = cam->taken;
		size_t cap = cam->takenCap;
		cam->taken = cam->pending;
		cam->takenCap = cam->pendingCap;
		cam->pending = p;
		cam->pendingCap = cap;
		cam->fresh = 0;
		*data = cam->taken;
		*w = cam->width;
		*h = cam->height;
		got = 1;
	}
	pthread_mutex_unlock(&cam->mu);
	if (!got) {
		@autoreleasepool {
			if (!cam.device.isConnected) return -1;
		}
	}
	return got;
}

static void dm_cam_close(void *handle) {
	@autoreleasepool {
		DMCamera *cam = (__bridge_transfer DMCamera *)handle;
		[cam.session stopRunning];
		// No frame is handed over after this: the queue is drained before the object goes.
		dispatch_sync(cam.queue, ^{});
	}
}
*/
import "C"

import (
	"context"
	"slices"
	"strings"
	"unsafe"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/media"
)

// waitMillis bounds how long Stop waits for the capture goroutine, which checks for cancellation between waits for a frame.
const waitMillis = 100

// Devices lists the cameras of the Mac: the built-in one, external ones and an iPhone used as a Continuity Camera. Listing them
// needs no permission and turns no camera on. It fails with the tag ListCamerasCancelled when ctx has ended.
func Devices(ctx context.Context) ([]Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.NewErr("ListCamerasCancelled", err)
	}
	list := C.dm_cam_devices()
	defer C.free(unsafe.Pointer(list))
	return parseDevices(C.GoString(list)), nil
}

// parseDevices reads the lines of "unique id<TAB>name" that the listing makes.
func parseDevices(list string) []Device {
	var devices []Device
	for _, line := range strings.Split(list, "\n") {
		id, name, ok := strings.Cut(line, "\t")
		if !ok || id == "" {
			continue
		}
		if name == "" {
			name = id
		}
		devices = append(devices, Device{ID: id, Description: name})
	}
	return devices
}

// stream is a camera that is capturing.
type stream struct {
	cam unsafe.Pointer
	id  string
}

// openStream asks for the permission to use the camera (the system asks the user the first time) and starts capturing. It fails
// with the tag CameraAccessDenied when the user said no, CameraNotFound for a camera that is not there, CameraOpenFailed when the
// system does not open it (another program holds it, for one) and CameraFormatUnsupported when it gives no frames the program
// reads.
func openStream(id string) (*stream, error) {
	// A camera that is not there is told before the user is asked for anything.
	list := C.dm_cam_devices()
	known := slices.ContainsFunc(parseDevices(C.GoString(list)), func(d Device) bool { return d.ID == id })
	C.free(unsafe.Pointer(list))
	if !known {
		return nil, errors.New("CameraNotFound", "the selected camera is no longer available", "id", id)
	}
	if C.dm_cam_authorize() == 0 {
		return nil, errors.New("CameraAccessDenied", "this program may not use the camera: allow it in System Settings, Privacy & Security, Camera", "id", id)
	}
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	var code C.int
	var reason *C.char
	cam := C.dm_cam_open(cid, &code, &reason)
	if cam == nil {
		switch code {
		case C.DM_CAM_NOT_FOUND:
			return nil, errors.New("CameraNotFound", "the selected camera is no longer available", "id", id)
		case C.DM_CAM_INPUT_FAILED:
			why := C.GoString(reason)
			C.free(unsafe.Pointer(reason))
			return nil, errors.New("CameraOpenFailed", "the camera could not be opened", "id", id, "reason", why)
		}
		return nil, errors.New("CameraFormatUnsupported", "the camera gives no video that the program can read", "id", id)
	}
	return &stream{cam: cam, id: id}, nil
}

// run hands the frames to onFrame until ctx ends. It fails with the tag CameraDisconnected when the camera is unplugged.
func (s *stream) run(ctx context.Context, onFrame func(media.RGBAFrame)) error {
	for ctx.Err() == nil {
		var data *C.uint8_t
		var w, h C.int
		switch C.dm_cam_wait(s.cam, waitMillis, &data, &w, &h) {
		case -1:
			return errors.New("CameraDisconnected", "the camera was unplugged", "id", s.id)
		case 1:
			if onFrame != nil {
				onFrame(media.RGBAFrame{Width: int(w), Height: int(h), Data: C.GoBytes(unsafe.Pointer(data), w*h*4)})
			}
		}
	}
	return nil
}

func (s *stream) close() {
	if s.cam != nil {
		C.dm_cam_close(s.cam)
		s.cam = nil
	}
}
