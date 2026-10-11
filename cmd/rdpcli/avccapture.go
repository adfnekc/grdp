package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/adfnekc/grdp/codec"
)

// avcCapture is an EGFX AVC decoder that does not decode.
//
// It exists to capture a bitstream: the client advertises exactly the AVC codecs
// its decoder reports, so without one a server never sends AVC at all and there
// is nothing to study. Claiming support makes the server send it, and writing the
// access units to disk produces the test vector that a real decoder has to be
// checked against.
//
// It returns a blank surface, so the session draws black. That is deliberate:
// nothing here decodes, and a capture run is not meant to be looked at.
type avcCapture struct {
	dir string

	mu       sync.Mutex
	units    int
	bytes    int64
	byCodec  map[uint16]int
	byStream map[uint8]int
}

func newAVCCapture(dir string) (*avcCapture, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &avcCapture{
		dir:      dir,
		byCodec:  map[uint16]int{},
		byStream: map[uint8]int{},
	}, nil
}

// SupportsAVCCodec claims every AVC codec, which is what makes the server offer
// them. A real decoder would claim only what it can decode.
func (a *avcCapture) SupportsAVCCodec(codecID uint16) bool { return true }

type streamDump struct {
	Index   int      `json:"index"`
	Bytes   int      `json:"bytes"`
	File    string   `json:"file"`
	Regions []string `json:"regions"`
	QPs     []uint8  `json:"qps,omitempty"`
}

type unitDump struct {
	Unit    int          `json:"unit"`
	CodecID string       `json:"codecID"`
	LC      uint8        `json:"lc"`
	Width   int          `json:"width"`
	Height  int          `json:"height"`
	Streams []streamDump `json:"streams"`
}

// DecodeAVC writes the access unit and returns blank pixels.
func (a *avcCapture) DecodeAVC(codecID uint16, au *codec.AVCAccessUnit, width, height int) ([]byte, error) {
	a.mu.Lock()
	a.units++
	index := a.units
	a.byCodec[codecID]++
	a.byStream[au.LC]++
	a.mu.Unlock()

	base := fmt.Sprintf("au-%05d", index)
	dump := unitDump{
		Unit:    index,
		CodecID: fmt.Sprintf("%#04x", codecID),
		LC:      au.LC,
		Width:   width,
		Height:  height,
	}
	var total int64
	for i := range au.Streams {
		st := &au.Streams[i]
		name := fmt.Sprintf("%s-s%d.h264", base, i)
		if err := os.WriteFile(filepath.Join(a.dir, name), st.Data, 0o644); err != nil {
			return nil, err
		}
		total += int64(len(st.Data))
		sd := streamDump{Index: i, Bytes: len(st.Data), File: name}
		for _, r := range st.Regions {
			sd.Regions = append(sd.Regions, fmt.Sprintf("%d,%d,%d,%d", r.Left, r.Top, r.Right, r.Bottom))
		}
		for _, q := range st.Quant {
			sd.QPs = append(sd.QPs, q.QP)
		}
		dump.Streams = append(dump.Streams, sd)
	}
	manifest, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(a.dir, base+".json"), append(manifest, '\n'), 0o644); err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.bytes += total
	a.mu.Unlock()

	// Blank rather than decoded: this is a capture, and a caller that looked at
	// the surface would be looking at nothing.
	return make([]byte, width*height*4), nil
}

// report prints what was captured, which is the evidence that the server sent
// AVC at all and in which shape.
func (a *avcCapture) report(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.units == 0 {
		fmt.Println("avc capture: the server sent no AVC surface updates")
		return
	}
	fmt.Printf("avc capture: %d access units, %d bytes of bitstream, written to %s\n",
		a.units, a.bytes, dir)
	for id, n := range a.byCodec {
		fmt.Printf("  codec %#04x: %d units\n", id, n)
	}
	for lc, n := range a.byStream {
		name := map[uint8]string{0: "luma and chroma", 1: "luma only", 2: "chroma only"}[lc]
		fmt.Printf("  lc=%d (%s): %d units\n", lc, name, n)
	}
}
