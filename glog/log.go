package glog

import (
	"fmt"
	"log"
	"sync"
)

var (
	logger *log.Logger
	level  LEVEL
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

func SetLogger(l *log.Logger) {
	l.SetFlags(log.Ldate | log.Ltime | log.Llongfile)
	logger = l
}

func SetLevel(l LEVEL) {
	level = l
}

func checkLogger() {
	if logger == nil && level != NONE {
		panic("logger not inited")
	}
}
func Trace(v ...interface{}) {
	checkLogger()
	if level <= TRACE {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[TRACE]")
		s := fmt.Sprintln(v...)
		if len(s) > 1024 {
			logger.Output(2, s[:1024]+"[truncate]......")
			return
		}
		logger.Output(2, s)
	}
}
func Tracef(f string, v ...interface{}) {
	checkLogger()
	if level <= TRACE {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[TRACE]")
		logger.Output(2, fmt.Sprintln(fmt.Sprintf(f, v...)))
	}
}
func Debug(v ...interface{}) {
	checkLogger()
	if level <= DEBUG {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[DEBUG]")
		logger.Output(2, fmt.Sprintln(v...))
	}
}
func Debugf(f string, v ...interface{}) {
	checkLogger()
	if level <= DEBUG {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[DEBUG]")
		logger.Output(2, fmt.Sprintln(fmt.Sprintf(f, v...)))
	}
}
func Info(v ...interface{}) {
	checkLogger()
	if level <= INFO {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[INFO]")
		logger.Output(2, fmt.Sprintln(v...))
	}
}
func Infof(f string, v ...interface{}) {
	checkLogger()
	if level <= INFO {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[INFO]")
		logger.Output(2, fmt.Sprintln(fmt.Sprintf(f, v...)))
	}
}
func Warn(v ...interface{}) {
	checkLogger()
	if level <= WARN {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[WARN]")
		logger.Output(2, fmt.Sprintln(v...))
	}
}
func Warnf(f string, v ...interface{}) {
	checkLogger()
	if level <= WARN {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[WARN]")
		logger.Output(2, fmt.Sprintln(fmt.Sprintf(f, v...)))
	}
}
func Error(v ...interface{}) {
	checkLogger()
	if level <= ERROR {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[ERROR]")
		logger.Output(2, fmt.Sprintln(v...))
	}
}
func Errorf(f string, v ...interface{}) {
	checkLogger()
	if level <= ERROR {
		mu.Lock()
		defer mu.Unlock()
		logger.SetPrefix("[ERROR]")
		logger.Output(2, fmt.Sprintln(fmt.Sprintf(f, v...)))
	}
}
