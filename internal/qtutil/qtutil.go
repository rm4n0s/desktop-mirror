// Package qtutil holds helpers for miqt's memory model: values returned by
// value from Qt carry a Go finalizer that deletes the C++ object, while
// objects from constructors must be deleted by hand.
package qtutil

import (
	"runtime"
	"runtime/debug"

	qt "github.com/mappu/miqt/qt6"
)

// Release frees img's C++ memory now. Images that come back by value from Qt
// (ToImage, Copy, ConvertToFormat, ...) have a finalizer, so it is cleared
// first to avoid a double free. The Go GC cannot see C++ memory, so waiting
// for the finalizer would let frame buffers pile up.
func Release(img *qt.QImage) {
	if img == nil {
		return
	}
	runtime.SetFinalizer(img, nil)
	img.Delete()
}

// WithGCPaused runs fn with the garbage collector disabled. miqt attaches the
// finalizer of a by-value array element to a pointer that is dropped as soon
// as the array is built, so the elements stay valid only until the next GC
// cycle; fn must finish using them (or copy what it needs) before returning.
// Call from the Qt main thread only.
func WithGCPaused(fn func()) {
	prev := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(prev)
	fn()
}
