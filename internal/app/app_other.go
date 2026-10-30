//go:build !darwin || nogui

// Package app shows the panel in a native window.
package app

import "errors"

// Supported reports whether this build opens a native window. Builds for
// other systems, and -tags nogui builds, show the panel in the browser.
const Supported = false

// Run is not available in this build.
func Run(url, title string) error {
	return errors.New("这个构建没有窗口，面板已在浏览器里打开")
}
