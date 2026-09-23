package glog

import (
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

type LEVEL int

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

func SetLevel(l LEVEL) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

func Level() LEVEL {
	mu.Lock()
	defer mu.Unlock()
	return level
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

func Trace(v ...interface{})                 { write(TRACE, "[TRACE]", fmt.Sprintln(v...)) }
func Tracef(f string, v ...interface{})      { write(TRACE, "[TRACE]", fmt.Sprintln(fmt.Sprintf(f, v...))) }
func Debug(v ...interface{})                 { write(DEBUG, "[DEBUG]", fmt.Sprintln(v...)) }
func Debugf(f string, v ...interface{})      { write(DEBUG, "[DEBUG]", fmt.Sprintln(fmt.Sprintf(f, v...))) }
func Info(v ...interface{})                  { write(INFO, "[INFO]", fmt.Sprintln(v...)) }
func Infof(f string, v ...interface{})       { write(INFO, "[INFO]", fmt.Sprintln(fmt.Sprintf(f, v...))) }
func Warn(v ...interface{})                  { write(WARN, "[WARN]", fmt.Sprintln(v...)) }
func Warnf(f string, v ...interface{})       { write(WARN, "[WARN]", fmt.Sprintln(fmt.Sprintf(f, v...))) }
func Error(v ...interface{})                 { write(ERROR, "[ERROR]", fmt.Sprintln(v...)) }
func Errorf(f string, v ...interface{})      { write(ERROR, "[ERROR]", fmt.Sprintln(fmt.Sprintf(f, v...))) }
