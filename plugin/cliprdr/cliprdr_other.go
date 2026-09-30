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

// Clipboard format names and ids used by the non-Windows clipboard surface.
const (
	CFSTR_FILEDESCRIPTORW = "FileGroupDescriptorW"
	CFSTR_FILECONTENTS    = "FileContents"

	CF_TEXT        = 1
	CF_UNICODETEXT = 13
	CF_HDROP       = 15
)

// File attribute flags used in a FILEDESCRIPTORW.
const (
	FILE_ATTRIBUTE_DIRECTORY = 0x00000010
	FILE_ATTRIBUTE_NORMAL    = 0x00000080
)

// Pseudo format ids for the named formats the client knows about. There is no
// OS clipboard registry here, so RegisterClipboardFormat hands out these stable
// values instead. They sit in the 0xC000-0xFFFF range Windows reserves for
// registered formats and cannot collide with the predefined CF_* ids.
const (
	registeredFormatFileGroupDescriptorW = 0xC000
	registeredFormatFileContents         = 0xC001
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

// OpenClipboard is a no-op that always succeeds; there is no OS clipboard.
func OpenClipboard(hwnd uintptr) bool { return true }

// CloseClipboard is a no-op that always succeeds.
func CloseClipboard() bool { return true }

// EmptyClipboard is a no-op that always succeeds; the application owns the
// contents through SetClipboardText.
func EmptyClipboard() bool { return true }

// SetClipboardData is a no-op that always succeeds.
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

// RegisterClipboardFormat returns the stable pseudo-id for a format this
// client knows how to produce, or zero for anything else. There is no OS
// clipboard registry on this platform.
func RegisterClipboardFormat(format string) uint32 {
	switch format {
	case CFSTR_FILEDESCRIPTORW:
		return registeredFormatFileGroupDescriptorW
	case CFSTR_FILECONTENTS:
		return registeredFormatFileContents
	default:
		return 0
	}
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

// localFormatList returns the formats the local clipboard can provide: text
// when the application published any, plus the file formats when a
// FileProvider is set. A format is advertised only when its data can actually
// be produced, so the pasting side never waits for data that will not come.
func (c *CliprdrClient) localFormatList() []CliprdrFormat {
	list := GetFormatList(c.hwnd)
	c.mu.Lock()
	hasProvider := c.fileProvider != nil
	c.mu.Unlock()
	if !hasProvider {
		return list
	}
	return append(list,
		CliprdrFormat{FormatId: registeredFormatFileGroupDescriptorW, FormatName: CFSTR_FILEDESCRIPTORW},
		CliprdrFormat{FormatId: registeredFormatFileContents, FormatName: CFSTR_FILECONTENTS},
	)
}

// platformClipboardFiles reports that this platform has no OS clipboard file
// source. Files to share must come from SetFileProvider.
func platformClipboardFiles() FileProvider { return nil }

// GetFileInfo is unused on this platform; it exists to satisfy the shared
// clipboard surface, which has no file metadata to report.
func GetFileInfo(sys interface{}) (uint32, []byte, uint32, uint32) { return 0, nil, 0, 0 }

// GetFileNames reports that this platform shares no files through an OS
// clipboard; SetFileProvider is the only file source.
func GetFileNames() []string { return nil }

// HmemAlloc is unused on this platform; there is no HGLOBAL to allocate.
func HmemAlloc(data []byte) uintptr { return 0 }
