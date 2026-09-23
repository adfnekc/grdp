//go:build !gl

// ui_headless.go
package main

import (
	"fmt"
	"os"

	"github.com/adfnekc/grdp/glog"
)

// StartUI is the headless placeholder used when the example is built without
// the "gl" build tag. The gxui-based desktop UI requires OpenGL and does not
// build with recent Go toolchains, so the default build stays dependency-free.
//
// To get the desktop UI: go build -tags gl ./example
// To run without a UI, use the web server mode: go run ./example -s
func StartUI(w, h int) {
	fmt.Fprintln(os.Stderr, "desktop UI is not built in.")
	fmt.Fprintln(os.Stderr, "  desktop UI : go build -tags gl ./example")
	fmt.Fprintln(os.Stderr, "  web server : go run ./example -s   (then open http://localhost:8088)")
	glog.Info("headless example: no UI attached")
}
