//go:build darwin && !nogui

// Package app shows the panel in a native window.
package app

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
int gcccode_app_run(const char *url, const char *title, int width, int height);
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"
)

func init() {
	// Cocoa wants the main thread; Go's main goroutine runs there until it is
	// unlocked, so keep it locked from init on.
	runtime.LockOSThread()
}

// Supported reports whether this build opens a native window.
const Supported = true

// Run opens url in a window titled title and blocks until the user closes
// it. It must be called from the main goroutine.
func Run(url, title string) error {
	curl, ctitle := C.CString(url), C.CString(title)
	defer C.free(unsafe.Pointer(curl))
	defer C.free(unsafe.Pointer(ctitle))
	if C.gcccode_app_run(curl, ctitle, 1180, 780) != 0 {
		return errors.New("打不开窗口")
	}
	return nil
}
