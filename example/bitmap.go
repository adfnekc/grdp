// bitmap.go
package main

import (
	"fmt"
	"runtime"
	"strconv"

	"github.com/adfnekc/grdp/core"
	"github.com/adfnekc/grdp/glog"
)

// Bitmap is a decoded screen update handed to the UI layer.
type Bitmap struct {
	DestLeft     int    `json:"destLeft"`
	DestTop      int    `json:"destTop"`
	DestRight    int    `json:"destRight"`
	DestBottom   int    `json:"destBottom"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	BitsPerPixel int    `json:"bitsPerPixel"`
	IsCompress   bool   `json:"isCompress"`
	Data         []byte `json:"data"`
}

// Control is the common surface implemented by both the RDP and VNC clients.
type Control interface {
	Login() error
	SetRequestedProtocol(p uint32)
	KeyUp(sc int, name string)
	KeyDown(sc int, name string)
	MouseMove(x, y int)
	MouseWheel(scroll, x, y int)
	MouseUp(button int, x, y int)
	MouseDown(button int, x, y int)
	Close()
}

// BitmapCH delivers decoded bitmaps from the protocol layer to the UI.
var BitmapCH chan []Bitmap

// ui_paint_bitmap is called by the protocol layer when a bitmap update arrives.
func ui_paint_bitmap(bs []Bitmap) {
	BitmapCH <- bs
}

func uiClient(info *Info) (error, Control) {
	runtime.GOMAXPROCS(runtime.NumCPU())

	var (
		err error
		g   Control
	)
	if true {
		err, g = uiRdp(info)
	} else {
		err, g = uiVnc(info)
	}

	return err, g
}

func Bpp(BitsPerPixel uint16) (pixel int) {
	switch BitsPerPixel {
	case 15:
		pixel = 1

	case 16:
		pixel = 2

	case 24:
		pixel = 3

	case 32:
		pixel = 4

	default:
		glog.Error("invalid bitmap data format")
	}
	return
}

func ToRGBA(pixel int, i int, data []byte) (r, g, b, a uint8) {
	a = 255
	switch pixel {
	case 1:
		rgb555 := core.Uint16BE(data[i], data[i+1])
		r, g, b = core.RGB555ToRGB(rgb555)
	case 2:
		rgb565 := core.Uint16BE(data[i], data[i+1])
		r, g, b = core.RGB565ToRGB(rgb565)
	case 3, 4:
		fallthrough
	default:
		r, g, b = data[i+2], data[i+1], data[i]
	}

	return
}

func Hex2Dec(val string) int {
	n, err := strconv.ParseUint(val, 16, 32)
	if err != nil {
		fmt.Println(err)
	}
	return int(n)
}
