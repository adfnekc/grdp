package glog

import (
	"bytes"
	"encoding/hex"
	"log"
	"strings"
	"testing"
)

// capture sends the package's output to a buffer and returns it, restoring the
// previous logger and level afterwards.
func capture(tb testing.TB, level LEVEL) *bytes.Buffer {
	tb.Helper()
	var buf bytes.Buffer
	oldLevel := Level()
	SetLogger(log.New(&buf, "", 0))
	SetLevel(level)
	tb.Cleanup(func() {
		SetLevel(oldLevel)
		SetLogger(nil)
	})
	return &buf
}

func TestLevelsFilterOutput(t *testing.T) {
	// Each level writes its own line and every line above it, so the level N test
	// is the absence of all five. The prefix is checked separately from the
	// message because the logger is configured with log.Llongfile, which puts the
	// file and line between them.
	for _, tc := range []struct {
		level LEVEL
		want  []string
	}{
		{TRACE, []string{"[TRACE]", "trace line", "[DEBUG]", "debug line", "[INFO]", "info line", "[WARN]", "warn line", "[ERROR]", "error line"}},
		{DEBUG, []string{"[DEBUG]", "debug line", "[ERROR]", "error line"}},
		{INFO, []string{"[INFO]", "info line"}},
		{WARN, []string{"[WARN]", "warn line"}},
		{ERROR, []string{"[ERROR]", "error line"}},
		{NONE, nil},
	} {
		buf := capture(t, tc.level)
		Trace("trace line")
		Debug("debug line")
		Info("info line")
		Warn("warn line")
		Error("error line")
		got := buf.String()
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("at level %d output %q does not contain %q", tc.level, got, want)
			}
		}
		if tc.level == NONE && got != "" {
			t.Errorf("at level NONE the output was %q, want nothing", got)
		}
		if tc.level == ERROR && strings.Contains(got, "warn") {
			t.Errorf("at level ERROR the output %q contains a WARN line", got)
		}
	}
}

func TestSetLoggerNilDiscards(t *testing.T) {
	buf := capture(t, TRACE)
	SetLogger(nil)
	Trace("should not panic or be written")
	Info("nor this")
	if buf.Len() != 0 {
		t.Errorf("a nil logger wrote %q", buf.String())
	}
}

// TestDisabledLevelDoesNotBuildTheMessage is the regression test for the cost
// this package used to impose on its callers: the message was formatted before
// the level was consulted, and a payload was encoded into a string at the call
// site, so every log call at every layer of the stack paid for a string that was
// then thrown away.
//
// The assertion is not that the call is free. A variadic call boxes its
// arguments, which costs one small allocation however the line is written; what
// it must not do is cost anything proportional to the payload, because that is
// what made a file transfer slower the larger the read. Bytes per call are
// measured for a payload four thousand times larger and must not move.
func TestDisabledLevelDoesNotBuildTheMessage(t *testing.T) {
	small := make([]byte, 256)
	large := make([]byte, 1<<20)
	capture(t, NONE)

	bytesPerCall := func(payload []byte) uint64 {
		r := testing.Benchmark(func(bench *testing.B) {
			for i := 0; i < bench.N; i++ {
				Trace("recvData", Hex(payload))
			}
		})
		return uint64(r.AllocedBytesPerOp())
	}

	smallCost, largeCost := bytesPerCall(small), bytesPerCall(large)
	if largeCost != smallCost {
		t.Errorf("a disabled Trace call cost %d bytes for a 256 byte payload and %d bytes for 1 MiB, want the same: the cost must not scale with the payload", smallCost, largeCost)
	}
	if largeCost > 1024 {
		t.Errorf("a disabled Trace call cost %d bytes, want well under the 1 MiB payload it was given", largeCost)
	}
}

// TestDisabledLevelDoesNotEncode either way is the same property for Tracef,
// whose format string used to be built and then discarded as well.
func TestDisabledLevelDoesNotEncodeEitherWay(t *testing.T) {
	payload := make([]byte, 1<<20)
	capture(t, NONE)
	r := testing.Benchmark(func(bench *testing.B) {
		for i := 0; i < bench.N; i++ {
			Tracef("recvData %s", Hex(payload))
		}
	})
	if got := uint64(r.AllocedBytesPerOp()); got > 1024 {
		t.Errorf("a disabled Tracef call cost %d bytes, want none of the payload's 1 MiB", got)
	}
}

// TestHexEncodesOnlyWhenWritten pins the other half: when the line is written,
// Hex must still produce the hexadecimal the callers used to build by hand.
func TestHexEncodesOnlyWhenWritten(t *testing.T) {
	buf := capture(t, TRACE)
	Trace("recv:", Hex([]byte{0xde, 0xad, 0xbe, 0xef}))
	if got, want := buf.String(), "deadbeef"; !strings.Contains(got, want) {
		t.Errorf("Hex produced %q, want it to contain %q", got, want)
	}

	buf.Reset()
	Trace("recv:", Hex(nil))
	if strings.Contains(buf.String(), "<nil>") {
		t.Errorf("a nil slice rendered as %q, want an empty encoding", buf.String())
	}
}

func TestHexMatchesEncoding(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {0x00}, {0xff, 0x00, 0x0a}, bytes.Repeat([]byte{0xab}, 300)} {
		if got, want := Hex(b).String(), hex.EncodeToString(b); got != want {
			t.Errorf("Hex(%x).String() = %q, want %q", b, got, want)
		}
	}
}

// BenchmarkDisabledTraceHex is the cost this package used to put on every
// received PDU. The two sub-benchmarks are the same line written the two ways
// that existed: the old one builds the string at the call site, the new one
// leaves it to Hex and never builds it at all.
func BenchmarkDisabledTraceHex(b *testing.B) {
	payload := make([]byte, 1<<20)
	oldLevel := Level()
	SetLogger(nil)
	SetLevel(NONE)
	b.Cleanup(func() {
		SetLevel(oldLevel)
	})

	b.Run("encoded at the call site", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Trace("tpkt recvData", hex.EncodeToString(payload))
		}
	})
	b.Run("deferred through Hex", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Trace("tpkt recvData", Hex(payload))
		}
	})
}
