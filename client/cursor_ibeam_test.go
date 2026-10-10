package client

import (
	"os"
	"testing"

	"github.com/adfnekc/grdp/protocol/pdu"
)

// These two masks were captured from Windows 10 LTSC 19041 while the pointer sat
// over a text field, where it shows the I beam. They are the whole reason the
// rule this test covers exists, so they are kept rather than made up.
func loadIBeam(t *testing.T) *pdu.PointerShape {
	t.Helper()
	xor, err := os.ReadFile("testdata/ibeam_32x32_24bpp.xor")
	if err != nil {
		t.Fatalf("reading the captured XOR mask: %v", err)
	}
	and, err := os.ReadFile("testdata/ibeam_32x32_24bpp.and")
	if err != nil {
		t.Fatalf("reading the captured AND mask: %v", err)
	}
	return &pdu.PointerShape{
		Width: 32, Height: 32, HotspotX: 8, HotspotY: 9,
		XorBpp: 3, Xor: xor, And: and,
	}
}

// The server sends this shape with almost the whole AND mask set, which cannot
// be a description of what is see through: it would erase the pointer. Its XOR
// mask holds a complete I beam, and that is what must be drawn.
func TestColourPointerWithUnusableAndMaskUsesTheXorPicture(t *testing.T) {
	s := loadIBeam(t)
	if !andMaskIsSetAlmostEverywhere(s.And, s.Width, s.Height) {
		t.Fatalf("the captured AND mask is not the case this test is about")
	}

	cur := cursorFromShape(s)
	if cur == nil {
		t.Fatal("the shape decoded to nothing")
	}

	opaque := 0
	for y := 0; y < cur.Height; y++ {
		for x := 0; x < cur.Width; x++ {
			if cur.Image.Pix[y*cur.Image.Stride+x*4+3] != 0 {
				opaque++
			}
		}
	}
	// The XOR mask has 46 pixels that are not the black background, but two of
	// them are a stray pair in the one row the AND mask clears, which are not
	// part of the pointer. Under the AND rule that used to run this came out as
	// 40 pixels in four dots.
	if opaque != 44 {
		t.Errorf("the I beam draws %d opaque pixels, want 44", opaque)
	}

	// It has to come out as an I beam: a vertical bar with a cap at each end,
	// which is what this shape is and what the four dots were not.
	column := func(x int) int {
		n := 0
		for y := 0; y < cur.Height; y++ {
			if cur.Image.Pix[y*cur.Image.Stride+x*4+3] != 0 {
				n++
			}
		}
		return n
	}
	row := func(y int) int {
		n := 0
		for x := 0; x < cur.Width; x++ {
			if cur.Image.Pix[y*cur.Image.Stride+x*4+3] != 0 {
				n++
			}
		}
		return n
	}
	if bar := column(11); bar < 12 {
		t.Errorf("the bar column is drawn for %d rows, want a bar", bar)
	}
	// The caps are eight pixels wide, and the rows right above the top cap and
	// right below the bottom one are empty.
	if cap := row(2); cap < 6 {
		t.Errorf("the top cap is %d pixels wide, want a cap", cap)
	}
	if cap := row(17); cap < 6 {
		t.Errorf("the bottom cap is %d pixels wide, want a cap", cap)
	}
	for _, y := range []int{0, 1, cur.Height - 1} {
		if n := row(y); n != 0 {
			t.Errorf("row %d has %d drawn pixels; the top of an I beam is empty and nothing is drawn below it", y, n)
		}
	}
}

// A colour pointer whose AND mask does describe the shape keeps the rule it had,
// which is the rule FreeRDP uses.
func TestColourPointerWithAUsableAndMaskKeepsTheAndRule(t *testing.T) {
	twoPixels := func(px [4]byte) []byte {
		return []byte{px[2], px[1], px[0], px[2], px[1], px[0]}
	}
	white := [4]byte{0xff, 0xff, 0xff, 0xff}
	// One row of two white pixels with the AND clear: both are drawn as they are.
	and := []byte{0x00, 0x00}
	s := &pdu.PointerShape{
		Width: 2, Height: 1, XorBpp: 3,
		Xor: twoPixels(white), And: and,
	}
	if andMaskIsSetAlmostEverywhere(s.And, s.Width, s.Height) {
		t.Fatal("a clear AND mask was taken for an unusable one")
	}
	cur := cursorFromShape(s)
	if cur == nil {
		t.Fatal("the shape decoded to nothing")
	}
	for x := 0; x < 2; x++ {
		o := x * 4
		if cur.Image.Pix[o+3] == 0 {
			t.Errorf("pixel %d is transparent, but its AND bit is clear", x)
		}
	}
}
