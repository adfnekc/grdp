// Command rdpcli is a small headless RDP client used to verify connectivity
// against a live server.
//
// Usage:
//
//	go run ./cmd/rdpcli -host 127.0.0.1:3389 -user rdptest -pass secret
//	go run ./cmd/rdpcli -host host:3389 -user u -pass p -wait 30s -bitmaps 3
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/adfnekc/grdp/client"
	"github.com/adfnekc/grdp/glog"
)

func main() {
	host := flag.String("host", "127.0.0.1:3389", "RDP server host:port")
	user := flag.String("user", "", "username (DOMAIN\\user or user)")
	pass := flag.String("pass", "", "password")
	proto := flag.String("proto", "auto", "security protocol: auto|tls|rdp")
	width := flag.Int("width", 1024, "desktop width")
	height := flag.Int("height", 768, "desktop height")
	wait := flag.Duration("wait", 20*time.Second, "how long to wait for the session to become ready")
	bitmaps := flag.Int("bitmaps", 0, "exit after this many bitmap updates (0 = wait until timeout)")
	logLevel := flag.Int("log", int(glog.INFO), "log level 0=TRACE..5=NONE")
	flag.Parse()

	s := client.NewSetting()
	s.Width = *width
	s.Height = *height
	s.Protocol = *proto
	s.LogLevel = glog.LEVEL(*logLevel)

	c := client.NewClient(*host, *user, *pass, client.TC_RDP, s)

	ready := make(chan struct{}, 1)
	failed := make(chan error, 1)
	closed := make(chan struct{}, 1)
	var bitmapCount int
	done := make(chan struct{}, 1)

	c.OnReady(func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	})
	c.OnError(func(err error) {
		select {
		case failed <- err:
		default:
		}
	})
	c.OnClose(func() {
		select {
		case closed <- struct{}{}:
		default:
		}
	})
	c.OnBitmap(func(bs []client.Bitmap) {
		bitmapCount += len(bs)
		fmt.Printf("bitmap update: %d rects (total %d)\n", len(bs), bitmapCount)
		if *bitmaps > 0 && bitmapCount >= *bitmaps {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	fmt.Printf("connecting to %s as %q ...\n", *host, *user)
	start := time.Now()
	if err := c.Login(); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("X.224/security handshake ok in %s, waiting for session ready...\n", time.Since(start).Round(time.Millisecond))

	timer := time.NewTimer(*wait)
	defer timer.Stop()

	for {
		select {
		case <-ready:
			fmt.Println("session ready")
		case err := <-failed:
			fmt.Fprintf(os.Stderr, "session error: %v\n", err)
			c.Close()
			os.Exit(2)
		case <-closed:
			fmt.Fprintln(os.Stderr, "connection closed by peer")
			os.Exit(3)
		case <-done:
			fmt.Printf("received %d bitmap rectangles, closing\n", bitmapCount)
			c.Close()
			return
		case <-timer.C:
			fmt.Printf("timeout after %s (bitmap rectangles received: %d)\n", *wait, bitmapCount)
			c.Close()
			if bitmapCount == 0 {
				os.Exit(4)
			}
			return
		}
	}
}
