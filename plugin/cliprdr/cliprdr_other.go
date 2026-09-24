//go:build !windows

package cliprdr

import (
	"sync"

	"github.com/adfnekc/grdp/glog"
)

// This file provides the non-Windows build of the platform clipboard surface
// required by cliprdr.go.
//
// Rather than binding to X11 or Wayland, the clipboard contents live in this
// process and the application decides what to put there: call
// CliprdrClient.SetClipboardText to publish text, and register a handler with
// OnText to receive what the server offers. That keeps the clipboard protocol
// usable from a headless client, where there is no display to own a selection
// on.

const (
	CFSTR_FILEDESCRIPTORW = "FileGroupDescriptorW"

	CF_TEXT        = 1
	CF_UNICODETEXT = 13
	CF_HDROP       = 15
)

const (
	FILE_ATTRIBUTE_DIRECTORY = 0x00000010
)

var (
	localClipMu   sync.Mutex
	localClipText string
	localClipSet  bool
)

// setLocalText publishes text as the local clipboard contents.
func setLocalText(s string) {
	localClipMu.Lock()
	defer localClipMu.Unlock()
	localClipText = s
	localClipSet = true
}

// localText returns the local clipboard text, if any.
func localText() (string, bool) {
	localClipMu.Lock()
	defer localClipMu.Unlock()
	return localClipText, localClipSet
}

// Control mirrors the Windows control window handle used by cliprdr.go.
type Control struct {
	hwnd uintptr
}

// withOpenClipboard runs f with the local clipboard "open". There is no OS
// clipboard handle here, so it just runs f.
func (c *Control) withOpenClipboard(f func()) {
	f()
}

// SendCliprdrMessage is a no-op on non-Windows platforms.
func (c *Control) SendCliprdrMessage() {}

// ClipWatcher would watch for local clipboard changes. There is nothing to
// watch without a display, so it returns immediately; applications drive the
// clipboard explicitly instead.
func ClipWatcher(c *CliprdrClient) {
	glog.Debug("cliprdr: no local clipboard watcher on this platform")
}

func OpenClipboard(hwnd uintptr) bool { return true }
func CloseClipboard() bool            { return true }
func EmptyClipboard() bool            { return true }
func SetClipboardData(uint32, uintptr) bool {
	// The application owns the clipboard through SetClipboardText.
	return true
}

// GetClipboardData returns the currently published text for either text
// format.
func GetClipboardData(formatId uint32) string {
	if formatId != CF_UNICODETEXT && formatId != CF_TEXT {
		return ""
	}
	s, _ := localText()
	return s
}

func RegisterClipboardFormat(format string) uint32 {
	// Return a stable pseudo-id so the rest of the protocol keeps working.
	return 0
}

// GetFormatList advertises text whenever the application has published any.
func GetFormatList(hwnd uintptr) []CliprdrFormat {
	if _, ok := localText(); !ok {
		return nil
	}
	return []CliprdrFormat{
		{FormatId: CF_UNICODETEXT},
		{FormatId: CF_TEXT},
	}
}

func GetFileInfo(sys interface{}) (uint32, []byte, uint32, uint32) { return 0, nil, 0, 0 }
func GetFileNames() []string                                       { return nil }
func HmemAlloc(data []byte) uintptr                                { return 0 }
