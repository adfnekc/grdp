// Package glog is the logging this library writes to a standard *log.Logger,
// with a level that can be set and discarded output by default.
//
// The level is consulted before a message is built, so a line that is not
// written costs nothing, and [Hex] defers the encoding of a byte dump to the
// same moment. Both matter on the per packet paths: a payload logged at TRACE
// used to be encoded into a string at the call site on every packet even when
// TRACE was off.
//
// Nothing is written until SetLogger is given a logger, which keeps the library
// quiet when embedded.
package glog

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"sync"
)

var (
	logger *log.Logger = log.New(io.Discard, "", 0)
	level  LEVEL       = INFO
	mu     sync.Mutex
)

// LEVEL is the priority of a line. A line is written when its level is at or
// above the level set by SetLevel, so TRACE writes everything and NONE nothing.
type LEVEL int

// The levels, from the most verbose to the least.
const (
	TRACE LEVEL = iota
	DEBUG
	INFO
	WARN
	ERROR
	NONE
)

// SetLogger sets the destination logger. A nil logger discards all output
// instead of panicking, so the library is safe to use out of the box.
func SetLogger(l *log.Logger) {
	mu.Lock()
	defer mu.Unlock()
	if l == nil {
		logger = log.New(io.Discard, "", 0)
		return
	}
	l.SetFlags(log.Ldate | log.Ltime | log.Llongfile)
	logger = l
}

// SetLevel sets the level at and above which lines are written. The default is
// INFO, and NONE turns the output off without changing the logger.
func SetLevel(l LEVEL) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// Level returns the level that SetLevel set, which is INFO until it is changed.
func Level() LEVEL {
	mu.Lock()
	defer mu.Unlock()
	return level
}

// enabled reports whether a line at level l would be written. It is checked
// before a message is built, so that a discarded line costs nothing.
func enabled(l LEVEL) bool {
	mu.Lock()
	defer mu.Unlock()
	return level <= l && logger != nil
}

// write emits a formatted line if the configured level permits it. Passing a
// nil logger (e.g. after SetLogger(nil)) is treated as "discard".
func write(l LEVEL, prefix, msg string) {
	mu.Lock()
	defer mu.Unlock()
	if level > l || logger == nil {
		return
	}
	logger.SetPrefix(prefix)
	logger.Output(3, msg)
}

// hexBytes is the deferred form of a hexadecimal dump. It exists so that a hot
// path can name a payload without encoding it; see [Hex].
type hexBytes []byte

func (h hexBytes) String() string { return hex.EncodeToString(h) }

// Hex returns a value that renders as the hexadecimal encoding of b.
//
// The encoding happens when the value is formatted, which only happens if the
// line is actually written. A caller that writes
//
//	glog.Trace("recvData", glog.Hex(payload))
//
// pays nothing for a payload that its level discards, which the equivalent
// glog.Hex(payload).String() at the call site does not: that encodes first and
// asks afterwards, on every packet, whether or not tracing is on. Use it for
// every byte dump on a path that runs per packet.
func Hex(b []byte) fmt.Stringer { return hexBytes(b) }

// Trace writes a line at TRACE, the most verbose level.
func Trace(v ...interface{}) {
	if !enabled(TRACE) {
		return
	}
	write(TRACE, "[TRACE]", fmt.Sprintln(v...))
}

// Tracef writes a formatted line at TRACE.
func Tracef(f string, v ...interface{}) {
	if !enabled(TRACE) {
		return
	}
	write(TRACE, "[TRACE]", fmt.Sprintln(fmt.Sprintf(f, v...)))
}

// Debug writes a line at DEBUG.
func Debug(v ...interface{}) {
	if !enabled(DEBUG) {
		return
	}
	write(DEBUG, "[DEBUG]", fmt.Sprintln(v...))
}

// Debugf writes a formatted line at DEBUG.
func Debugf(f string, v ...interface{}) {
	if !enabled(DEBUG) {
		return
	}
	write(DEBUG, "[DEBUG]", fmt.Sprintln(fmt.Sprintf(f, v...)))
}

// Info writes a line at INFO.
func Info(v ...interface{}) {
	if !enabled(INFO) {
		return
	}
	write(INFO, "[INFO]", fmt.Sprintln(v...))
}

// Infof writes a formatted line at INFO.
func Infof(f string, v ...interface{}) {
	if !enabled(INFO) {
		return
	}
	write(INFO, "[INFO]", fmt.Sprintln(fmt.Sprintf(f, v...)))
}

// Warn writes a line at WARN.
func Warn(v ...interface{}) {
	if !enabled(WARN) {
		return
	}
	write(WARN, "[WARN]", fmt.Sprintln(v...))
}

// Warnf writes a formatted line at WARN.
func Warnf(f string, v ...interface{}) {
	if !enabled(WARN) {
		return
	}
	write(WARN, "[WARN]", fmt.Sprintln(fmt.Sprintf(f, v...)))
}

// Error writes a line at ERROR.
func Error(v ...interface{}) {
	if !enabled(ERROR) {
		return
	}
	write(ERROR, "[ERROR]", fmt.Sprintln(v...))
}

// Errorf writes a formatted line at ERROR.
func Errorf(f string, v ...interface{}) {
	if !enabled(ERROR) {
		return
	}
	write(ERROR, "[ERROR]", fmt.Sprintln(fmt.Sprintf(f, v...)))
}
