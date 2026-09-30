package cliprdr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/adfnekc/grdp/core"
)

// testFileProvider is an in-memory FileProvider for the file transfer tests.
type testFileProvider struct {
	names []string
	info  map[string]testFileInfo
	data  map[string][]byte
}

type testFileInfo struct {
	size    int64
	modTime time.Time
	isDir   bool
}

func newTestFileProvider() *testFileProvider {
	return &testFileProvider{
		info: make(map[string]testFileInfo),
		data: make(map[string][]byte),
	}
}

func (p *testFileProvider) addFile(name string, data []byte) {
	p.names = append(p.names, name)
	p.info[name] = testFileInfo{size: int64(len(data)), modTime: time.Unix(1700000000, 0)}
	p.data[name] = data
}

func (p *testFileProvider) addDir(name string) {
	p.names = append(p.names, name)
	p.info[name] = testFileInfo{isDir: true, modTime: time.Unix(1700000000, 0)}
}

func (p *testFileProvider) Names() []string { return p.names }

func (p *testFileProvider) Info(name string) (int64, time.Time, bool) {
	i := p.info[name]
	return i.size, i.modTime, i.isDir
}

func (p *testFileProvider) Open(name string) (io.ReadCloser, error) {
	d, ok := p.data[name]
	if !ok {
		return nil, fmt.Errorf("no data for %q", name)
	}
	return io.NopCloser(bytes.NewReader(d)), nil
}

// namedFormatList encodes a server format list, with the NUL terminated names
// the file formats carry.
func namedFormatList(entries ...CliprdrFormat) []byte {
	var b []byte
	for _, e := range entries {
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], e.FormatId)
		b = append(b, tmp[:]...)
		if e.FormatName != "" {
			b = append(b, core.UnicodeEncode(e.FormatName)...)
		}
		b = append(b, 0, 0)
	}
	return b
}

// parseFormatList returns the format ids of a CB_FORMAT_LIST PDU.
func parseFormatList(t *testing.T, pdu []byte) []uint32 {
	t.Helper()
	if len(pdu) < 8 {
		t.Fatalf("format list PDU is %d bytes", len(pdu))
	}
	r := bytes.NewReader(pdu[8:])
	var ids []uint32
	for r.Len() > 0 {
		var id uint32
		if err := binary.Read(r, binary.LittleEndian, &id); err != nil {
			t.Fatalf("reading format id: %v", err)
		}
		ids = append(ids, id)
		for r.Len() > 0 {
			var u uint16
			if err := binary.Read(r, binary.LittleEndian, &u); err != nil {
				t.Fatalf("reading format name: %v", err)
			}
			if u == 0 {
				break
			}
		}
	}
	return ids
}

func containsID(ids []uint32, want uint32) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func lastSent(s *captureSender) []byte {
	chunks := s.sent()
	if len(chunks) == 0 {
		return nil
	}
	return chunks[len(chunks)-1]
}

// waitForMessage polls the captured PDUs for one of the given type.
func waitForMessage(t *testing.T, s *captureSender, msgType uint16) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, pdu := range s.sent() {
			if len(pdu) >= 2 && binary.LittleEndian.Uint16(pdu) == msgType {
				return pdu
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no message of type 0x%x was sent", msgType)
	return nil
}

// buildDescriptorList assembles a FILEGROUPDESCRIPTORW from descriptors.
func buildDescriptorList(entries ...FileDescriptor) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(len(entries)))
	for i := range entries {
		b = append(b, entries[i].serialize()...)
	}
	return b
}

// descriptorBytes hand-assembles a FILEDESCRIPTORW at the wire offsets, so a
// test can check the parser without going through the serializer that it is
// meant to verify.
func descriptorBytes(name string, attrs, sizeHigh, sizeLow uint32) []byte {
	b := make([]byte, fileDescriptorSize)
	binary.LittleEndian.PutUint32(b[0:], FD_ATTRIBUTES|FD_FILESIZE)
	binary.LittleEndian.PutUint32(b[36:], attrs)
	binary.LittleEndian.PutUint32(b[64:], sizeHigh)
	binary.LittleEndian.PutUint32(b[68:], sizeLow)
	copy(b[72:], core.UnicodeEncode(name))
	return b
}

func fileContentsRequest(streamID, index, flags uint32, off int64, cb uint32) []byte {
	b := make([]byte, 0, 28)
	put := func(v uint32) {
		var t [4]byte
		binary.LittleEndian.PutUint32(t[:], v)
		b = append(b, t[:]...)
	}
	put(streamID)
	put(index)
	put(flags)
	put(uint32(uint64(off) & 0xffffffff))
	put(uint32(uint64(off) >> 32))
	put(cb)
	put(0)
	return b
}

// requestLocalDescriptors asks the client to serve its file manifest and
// returns the parsed FILEGROUPDESCRIPTORW from the response.
func requestLocalDescriptors(t *testing.T, c *CliprdrClient, sent *captureSender) *FileGroupDescriptor {
	t.Helper()
	sent.reset()
	c.Process(msg(CB_FORMAT_DATA_REQUEST, 0, func() []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, RegisterClipboardFormat(CFSTR_FILEDESCRIPTORW))
		return b
	}()))
	resp := lastSent(sent)
	if resp == nil || binary.LittleEndian.Uint16(resp) != CB_FORMAT_DATA_RESPONSE {
		t.Fatalf("no format data response: %v", resp)
	}
	var fgd FileGroupDescriptor
	if err := fgd.Unpack(resp[8:]); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	return &fgd
}

// TestFormatListAdvertisesFilesOnlyWithProvider checks requirement 1: the file
// formats appear only while a FileProvider is set and disappear again when it
// is cleared.
func TestFormatListAdvertisesFilesOnlyWithProvider(t *testing.T) {
	c, sent := newTestClient()
	if err := c.SetClipboardText("x"); err != nil {
		t.Fatalf("SetClipboardText: %v", err)
	}
	sent.reset()
	c.Process(msg(CB_MONITOR_READY, 0, nil))

	// Without a provider the list holds only the two text formats.
	ids := parseFormatList(t, sent.sent()[1])
	if len(ids) != 2 || ids[0] != CF_UNICODETEXT || ids[1] != CF_TEXT {
		t.Fatalf("without a provider the list is %v", ids)
	}

	p := newTestFileProvider()
	p.addFile("hello.txt", []byte("hello"))
	c.SetFileProvider(p)

	ids = parseFormatList(t, lastSent(sent))
	if !containsID(ids, registeredFormatFileGroupDescriptorW) || !containsID(ids, registeredFormatFileContents) {
		t.Fatalf("with a provider the list is %v, want the file formats too", ids)
	}

	c.SetFileProvider(nil)
	ids = parseFormatList(t, lastSent(sent))
	if containsID(ids, registeredFormatFileGroupDescriptorW) || containsID(ids, registeredFormatFileContents) {
		t.Fatalf("after clearing the provider the list is %v, want no file formats", ids)
	}
}

// TestFileGroupDescriptorUnpackBounds checks that the wire count is validated
// before it can drive an allocation, and that a truncated entry is an error.
func TestFileGroupDescriptorUnpackBounds(t *testing.T) {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, 0xFFFFFFFF)
	var fgd FileGroupDescriptor
	if err := fgd.Unpack(b); err == nil {
		t.Fatal("a count of 0xFFFFFFFF must be rejected")
	}

	b = make([]byte, 4+fileDescriptorSize/2)
	binary.LittleEndian.PutUint32(b, 1)
	if err := fgd.Unpack(b); err == nil {
		t.Fatal("a truncated descriptor must be rejected")
	}

	if err := fgd.Unpack([]byte{1, 2}); err == nil {
		t.Fatal("a buffer shorter than the count must be rejected")
	}
}

// TestFileGroupDescriptorUnpackAlignsMultipleEntries feeds hand-built bytes for
// two descriptors, a file and a directory, to prove the fixed size name array
// keeps the entries aligned.
func TestFileGroupDescriptorUnpackAlignsMultipleEntries(t *testing.T) {
	file := descriptorBytes("alpha.txt", FILE_ATTRIBUTE_NORMAL, 1, 2)
	dir := descriptorBytes("beta", FILE_ATTRIBUTE_DIRECTORY, 0, 0)
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 2)
	raw = append(raw, file...)
	raw = append(raw, dir...)

	var fgd FileGroupDescriptor
	if err := fgd.Unpack(raw); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if fgd.CItems != 2 || len(fgd.Fgd) != 2 {
		t.Fatalf("got %d items", len(fgd.Fgd))
	}
	if got := decodeFileName(fgd.Fgd[0].FileName); got != "alpha.txt" {
		t.Fatalf("first name is %q", got)
	}
	if fgd.Fgd[0].isDir() {
		t.Fatal("alpha.txt reported as a directory")
	}
	if fgd.Fgd[0].FileSizeHigh != 1 || fgd.Fgd[0].FileSizeLow != 2 {
		t.Fatalf("alpha.txt size halves are %d/%d", fgd.Fgd[0].FileSizeHigh, fgd.Fgd[0].FileSizeLow)
	}
	if got := decodeFileName(fgd.Fgd[1].FileName); got != "beta" {
		t.Fatalf("second name is %q (entries are misaligned)", got)
	}
	if !fgd.Fgd[1].isDir() {
		t.Fatal("beta did not keep its directory attribute")
	}
}

// TestDescriptorSerializationSize checks the serializer emits the 592 byte
// FILEDESCRIPTORW the wire format defines, name array included.
func TestDescriptorSerializationSize(t *testing.T) {
	d, err := newFileDescriptor("a", 5, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	if got := len(d.serialize()); got != fileDescriptorSize {
		t.Fatalf("serialized descriptor is %d bytes, want %d", got, fileDescriptorSize)
	}
}

// TestDescriptorSizeHalves checks requirement 3: a size larger than 32 bits
// lands in the right halves, and a directory reports zero.
func TestDescriptorSizeHalves(t *testing.T) {
	const size = int64(1)<<33 + 7
	d, err := newFileDescriptor("big.bin", size, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	if d.FileSizeHigh != 2 || d.FileSizeLow != 7 {
		t.Fatalf("size halves are %d/%d, want 2/7", d.FileSizeHigh, d.FileSizeLow)
	}
	dir, err := newFileDescriptor("d", size, time.Unix(1700000000, 0), true)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	if dir.FileSizeHigh != 0 || dir.FileSizeLow != 0 {
		t.Fatalf("directory size halves are %d/%d, want 0/0", dir.FileSizeHigh, dir.FileSizeLow)
	}
	if !dir.isDir() {
		t.Fatal("directory descriptor lost its attribute")
	}
}

// TestServesFileDescriptorsFromProvider checks requirement 3's descriptor side:
// the manifest comes from the provider, directory entries included.
func TestServesFileDescriptorsFromProvider(t *testing.T) {
	c, sent := newTestClient()
	p := newTestFileProvider()
	p.addFile("hello.txt", []byte("hello world"))
	p.addDir("subdir")
	c.SetFileProvider(p)

	fgd := requestLocalDescriptors(t, c, sent)
	if fgd.CItems != 2 {
		t.Fatalf("manifest has %d entries, want 2", fgd.CItems)
	}
	if got := decodeFileName(fgd.Fgd[0].FileName); got != "hello.txt" {
		t.Fatalf("first entry is %q", got)
	}
	if fgd.Fgd[0].FileSizeLow != 11 || fgd.Fgd[0].FileSizeHigh != 0 {
		t.Fatalf("hello.txt size is %d/%d, want 0/11", fgd.Fgd[0].FileSizeHigh, fgd.Fgd[0].FileSizeLow)
	}
	if got := decodeFileName(fgd.Fgd[1].FileName); got != "subdir" {
		t.Fatalf("second entry is %q", got)
	}
	if !fgd.Fgd[1].isDir() {
		t.Fatal("subdir is not reported as a directory")
	}
}

// TestServesFileContentsRange feeds FILECONTENTS_RANGE byte requests and checks
// the slicing, including the end of file and a request past it.
func TestServesFileContentsRange(t *testing.T) {
	c, sent := newTestClient()
	p := newTestFileProvider()
	p.addFile("hello.txt", []byte("hello world"))
	c.SetFileProvider(p)
	requestLocalDescriptors(t, c, sent)

	cases := []struct {
		name string
		off  int64
		n    uint32
		want string
		ok   bool
	}{
		{"first five", 0, 5, "hello", true},
		{"middle", 6, 5, "world", true},
		{"whole file", 0, 11, "hello world", true},
		{"at end", 11, 5, "", true},
		{"past end", 12, 5, "", false},
		{"across end", 9, 100, "ld", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent.reset()
			c.Process(msg(CB_FILECONTENTS_REQUEST, 0,
				fileContentsRequest(42, 0, FILECONTENTS_RANGE, tc.off, tc.n)))
			resp := lastSent(sent)
			if resp == nil || binary.LittleEndian.Uint16(resp) != CB_FILECONTENTS_RESPONSE {
				t.Fatalf("no file contents response: %v", resp)
			}
			flags := binary.LittleEndian.Uint16(resp[2:])
			if tc.ok {
				if flags != CB_RESPONSE_OK {
					t.Fatalf("flags are 0x%x, want OK", flags)
				}
				if got := string(resp[12:]); got != tc.want {
					t.Fatalf("range is %q, want %q", got, tc.want)
				}
			} else if flags != CB_RESPONSE_FAIL {
				t.Fatalf("flags are 0x%x, want FAIL for a range past the end", flags)
			}
		})
	}
}

// TestServesFileContentsSize checks FILECONTENTS_SIZE returns the 64 bit size
// from Info, in little endian halves.
func TestServesFileContentsSize(t *testing.T) {
	c, sent := newTestClient()
	p := newTestFileProvider()
	c.SetFileProvider(p)
	p.names = append(p.names, "big.bin")
	p.info["big.bin"] = testFileInfo{size: int64(1)<<33 + 7, modTime: time.Unix(1700000000, 0)}
	requestLocalDescriptors(t, c, sent)

	sent.reset()
	c.Process(msg(CB_FILECONTENTS_REQUEST, 0, fileContentsRequest(7, 0, FILECONTENTS_SIZE, 0, 8)))
	resp := lastSent(sent)
	low := binary.LittleEndian.Uint32(resp[12:16])
	high := binary.LittleEndian.Uint32(resp[16:20])
	if int64(high)<<32|int64(low) != int64(1)<<33+7 {
		t.Fatalf("size is %d/%d", high, low)
	}
}

// TestFileDescriptorRequestWithoutProviderFails checks that a request the
// client cannot answer fails rather than returning an empty manifest.
func TestFileDescriptorRequestWithoutProviderFails(t *testing.T) {
	c, sent := newTestClient()
	sent.reset()
	c.Process(msg(CB_FORMAT_DATA_REQUEST, 0, func() []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, RegisterClipboardFormat(CFSTR_FILEDESCRIPTORW))
		return b
	}()))
	resp := lastSent(sent)
	if resp == nil || binary.LittleEndian.Uint16(resp) != CB_FORMAT_DATA_RESPONSE {
		t.Fatalf("no format data response: %v", resp)
	}
	if got := binary.LittleEndian.Uint16(resp[2:]); got != CB_RESPONSE_FAIL {
		t.Fatalf("flags are 0x%x, want FAIL", got)
	}
}

// deliverRemoteManifest walks the remote -> local announcement and descriptor
// response and returns the manifest the handler received.
func deliverRemoteManifest(t *testing.T, c *CliprdrClient, sent *captureSender, entries ...FileDescriptor) []RemoteFile {
	t.Helper()
	var got []RemoteFile
	c.OnRemoteFiles(func(files []RemoteFile) { got = files })

	sent.reset()
	c.Process(msg(CB_FORMAT_LIST, 0, namedFormatList(
		CliprdrFormat{FormatId: registeredFormatFileGroupDescriptorW, FormatName: CFSTR_FILEDESCRIPTORW},
		CliprdrFormat{FormatId: registeredFormatFileContents, FormatName: CFSTR_FILECONTENTS},
	)))
	req := waitForMessage(t, sent, CB_FORMAT_DATA_REQUEST)
	if id := binary.LittleEndian.Uint32(req[8:]); id != registeredFormatFileGroupDescriptorW {
		t.Fatalf("requested format 0x%x, want 0x%x", id, registeredFormatFileGroupDescriptorW)
	}

	c.Process(msg(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, buildDescriptorList(entries...)))
	if got == nil {
		t.Fatal("OnRemoteFiles was not called")
	}
	return got
}

// TestOnRemoteFilesReportsDirectories checks requirements 4 and 5: the parsed
// manifest is handed to OnRemoteFiles and a directory is flagged truthfully.
func TestOnRemoteFilesReportsDirectories(t *testing.T) {
	c, sent := newTestClient()
	file, err := newFileDescriptor("f.bin", 6, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	dir, err := newFileDescriptor("d", 0, time.Unix(1700000000, 0), true)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}

	got := deliverRemoteManifest(t, c, sent, file, dir)
	if len(got) != 2 {
		t.Fatalf("manifest has %d entries", len(got))
	}
	if got[0].Name != "f.bin" || got[0].Size != 6 || got[0].IsDir {
		t.Fatalf("first entry is %+v", got[0])
	}
	if got[1].Name != "d" || got[1].Size != 0 || !got[1].IsDir {
		t.Fatalf("directory entry is %+v", got[1])
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Fatalf("indices are %d and %d", got[0].Index, got[1].Index)
	}
}

// waitForFormatDataRequest polls the captured PDUs for a CB_FORMAT_DATA_REQUEST
// of the given format id.
func waitForFormatDataRequest(t *testing.T, s *captureSender, id uint32) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, pdu := range s.sent() {
			if len(pdu) >= 12 && binary.LittleEndian.Uint16(pdu) == CB_FORMAT_DATA_REQUEST &&
				binary.LittleEndian.Uint32(pdu[8:]) == id {
				return pdu
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no format data request for 0x%x was sent", id)
	return nil
}

// TestTextRequestWaitsForFileDescriptor covers the protocol constraint that a
// FORMAT_DATA_RESPONSE carries no format id: when the application handles both
// files and text, the descriptor list is fetched first and the text request
// follows once it has been answered.
func TestTextRequestWaitsForFileDescriptor(t *testing.T) {
	old := clipboardRequestDelay
	clipboardRequestDelay = time.Millisecond
	defer func() { clipboardRequestDelay = old }()

	c, sent := newTestClient()

	var text string
	c.OnText(func(s string) { text = s })
	var manifest []RemoteFile
	c.OnRemoteFiles(func(files []RemoteFile) { manifest = files })

	file, err := newFileDescriptor("f.bin", 1, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}

	sent.reset()
	c.Process(msg(CB_FORMAT_LIST, 0, namedFormatList(
		CliprdrFormat{FormatId: CF_UNICODETEXT},
		CliprdrFormat{FormatId: registeredFormatFileGroupDescriptorW, FormatName: CFSTR_FILEDESCRIPTORW},
	)))

	// The descriptor list is requested first; the text request is held back.
	waitForFormatDataRequest(t, sent, registeredFormatFileGroupDescriptorW)

	c.Process(msg(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, buildDescriptorList(file)))
	if manifest == nil {
		t.Fatal("OnRemoteFiles was not called")
	}

	// Only now is the text request sent, and its response reaches OnText.
	waitForFormatDataRequest(t, sent, CF_UNICODETEXT)
	c.Process(msg(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, append(core.UnicodeEncode("hi"), 0, 0)))
	if text != "hi" {
		t.Fatalf("OnText got %q, want %q", text, "hi")
	}
}

// answerRange sits in for the server: it waits for a FILECONTENTS_RANGE
// request, parses the stream id and replies with data.
func answerRange(t *testing.T, c *CliprdrClient, sent *captureSender, data string) {
	t.Helper()
	sent.reset()
	req := waitForMessage(t, sent, CB_FILECONTENTS_REQUEST)
	if flags := binary.LittleEndian.Uint32(req[16:]); flags != FILECONTENTS_RANGE {
		t.Fatalf("request flags are 0x%x, want RANGE", flags)
	}
	streamID := binary.LittleEndian.Uint32(req[8:12])
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, streamID)
	body = append(body, []byte(data)...)
	c.Process(msg(CB_FILECONTENTS_RESPONSE, CB_RESPONSE_OK, body))
}

// TestReadRemoteFile checks requirement 6: a remote range is fetched on demand
// and answered with exactly the requested slice.
func TestReadRemoteFile(t *testing.T) {
	old := remoteFileTimeout
	remoteFileTimeout = 500 * time.Millisecond
	defer func() { remoteFileTimeout = old }()

	c, sent := newTestClient()
	file, err := newFileDescriptor("f.bin", 6, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	deliverRemoteManifest(t, c, sent, file)

	type result struct {
		data []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := c.ReadRemoteFile(0, 2, 3)
		resCh <- result{data, err}
	}()

	answerRange(t, c, sent, "cde")

	res := <-resCh
	if res.err != nil {
		t.Fatalf("ReadRemoteFile: %v", res.err)
	}
	if string(res.data) != "cde" {
		t.Fatalf("got %q, want %q", res.data, "cde")
	}
}

// TestReadRemoteFileToEnd checks the n <= 0 case and the request it produces.
func TestReadRemoteFileToEnd(t *testing.T) {
	old := remoteFileTimeout
	remoteFileTimeout = 500 * time.Millisecond
	defer func() { remoteFileTimeout = old }()

	c, sent := newTestClient()
	file, err := newFileDescriptor("f.bin", 6, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	deliverRemoteManifest(t, c, sent, file)

	type result struct {
		data []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := c.ReadRemoteFile(0, 1, 0)
		resCh <- result{data, err}
	}()

	sent.reset()
	req := waitForMessage(t, sent, CB_FILECONTENTS_REQUEST)
	if got := binary.LittleEndian.Uint32(req[28:]); got != 5 {
		t.Fatalf("requested %d bytes, want the 5 remaining", got)
	}
	streamID := binary.LittleEndian.Uint32(req[8:12])
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, streamID)
	body = append(body, []byte("bcdef")...)
	c.Process(msg(CB_FILECONTENTS_RESPONSE, CB_RESPONSE_OK, body))

	res := <-resCh
	if res.err != nil || string(res.data) != "bcdef" {
		t.Fatalf("got %q, %v", res.data, res.err)
	}
}

// TestReadRemoteFileBounds checks requirement 7: a request past the end, a
// directory and a stale index are all errors, not short reads.
func TestReadRemoteFileBounds(t *testing.T) {
	c, sent := newTestClient()
	file, err := newFileDescriptor("f.bin", 6, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	dir, err := newFileDescriptor("d", 0, time.Unix(1700000000, 0), true)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	deliverRemoteManifest(t, c, sent, file, dir)

	if _, err := c.ReadRemoteFile(0, 7, 1); err == nil {
		t.Fatal("an offset past the end must be an error")
	}
	if _, err := c.ReadRemoteFile(0, -1, 1); err == nil {
		t.Fatal("a negative offset must be an error")
	}
	if _, err := c.ReadRemoteFile(1, 0, 1); err == nil {
		t.Fatal("a directory must not be readable")
	}
	if _, err := c.ReadRemoteFile(9, 0, 1); err == nil {
		t.Fatal("an index outside the manifest must be an error")
	}

	// A single read to the end larger than the cap is refused rather than
	// allocated.
	big, err := newFileDescriptor("big", maxRemoteRangeBytes+1, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	deliverRemoteManifest(t, c, sent, big)
	if _, err := c.ReadRemoteFile(0, 0, 0); err == nil {
		t.Fatal("a read to the end larger than the cap must be an error")
	}

	c.ClearRemoteFiles()
	if _, err := c.ReadRemoteFile(0, 0, 1); err == nil {
		t.Fatal("after ClearRemoteFiles the manifest must be gone")
	}
}

// TestReadRemoteFileFailureIsError checks that a refused range becomes an error
// instead of an empty success.
func TestReadRemoteFileFailureIsError(t *testing.T) {
	old := remoteFileTimeout
	remoteFileTimeout = 500 * time.Millisecond
	defer func() { remoteFileTimeout = old }()

	c, sent := newTestClient()
	file, err := newFileDescriptor("f.bin", 6, time.Unix(1700000000, 0), false)
	if err != nil {
		t.Fatalf("newFileDescriptor: %v", err)
	}
	deliverRemoteManifest(t, c, sent, file)

	resCh := make(chan error, 1)
	go func() {
		_, err := c.ReadRemoteFile(0, 0, 6)
		resCh <- err
	}()

	sent.reset()
	req := waitForMessage(t, sent, CB_FILECONTENTS_REQUEST)
	streamID := binary.LittleEndian.Uint32(req[8:12])
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, streamID)
	c.Process(msg(CB_FILECONTENTS_RESPONSE, CB_RESPONSE_FAIL, body))

	if err := <-resCh; err == nil {
		t.Fatal("a failed response must surface as an error")
	}
}
