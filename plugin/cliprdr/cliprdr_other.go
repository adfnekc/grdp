//go:build !windows

package cliprdr

import (
	"github.com/adfnekc/grdp/glog"
)

// This file provides the non-Windows build of the platform clipboard surface
// required by cliprdr.go. It is intentionally a no-op implementation so the
// package compiles and the RDP clipboard protocol can still be spoken.
//
// TODO(phase4): back these functions with a real Linux clipboard
// implementation (wl-clipboard / xclip / X11 selection) so text clipboard
// sync works cross-platform. See the development plan, Phase 4.

const (
	CFSTR_FILEDESCRIPTORW = "FileGroupDescriptorW"

	CF_TEXT        = 1
	CF_UNICODETEXT = 13
	CF_HDROP       = 15
)

const (
	FILE_ATTRIBUTE_DIRECTORY = 0x00000010
)

// Control mirrors the Windows control window handle used by cliprdr.go.
type Control struct {
	hwnd uintptr
}

// withOpenClipboard runs f with the local clipboard "open". On non-Windows
// platforms there is no OS clipboard handle, so it simply runs f.
func (c *Control) withOpenClipboard(f func()) {
	f()
}

// SendCliprdrMessage is a no-op on non-Windows platforms.
func (c *Control) SendCliprdrMessage() {}

// ClipWatcher blocks watching for local clipboard changes. The non-Windows
// implementation has no local clipboard integration yet and returns
// immediately.
func ClipWatcher(c *CliprdrClient) {
	glog.Debug("cliprdr: local clipboard watcher not implemented on this platform")
}

func OpenClipboard(hwnd uintptr) bool {
	return true
}

func CloseClipboard() bool {
	return true
}

func EmptyClipboard() bool {
	return true
}

func SetClipboardData(formatId uint32, hmem uintptr) bool {
	glog.Debug("cliprdr: SetClipboardData not implemented on this platform")
	return false
}

func GetClipboardData(formatId uint32) string {
	glog.Debug("cliprdr: GetClipboardData not implemented on this platform")
	return ""
}

func RegisterClipboardFormat(format string) uint32 {
	// Return a stable pseudo-id so the rest of the protocol keeps working.
	return 0
}

func GetFormatList(hwnd uintptr) []CliprdrFormat {
	return make([]CliprdrFormat, 0)
}

func GetFileInfo(sys interface{}) (uint32, []byte, uint32, uint32) {
	return 0, nil, 0, 0
}

func GetFileNames() []string {
	return nil
}

func HmemAlloc(data []byte) uintptr {
	return 0
}
