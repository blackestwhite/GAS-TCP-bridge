package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return Debug, nil
	case "", "info":
		return Info, nil
	case "warn", "warning":
		return Warn, nil
	case "error":
		return Error, nil
	default:
		return Info, fmt.Errorf("unknown log level %q", s)
	}
}

type Logger struct {
	min Level
	log *log.Logger
}

func New(level Level) *Logger {
	return NewWithWriter(level, os.Stderr)
}

func NewWithWriter(level Level, out io.Writer) *Logger {
	return &Logger{
		min: level,
		log: log.New(out, "", log.LstdFlags),
	}
}

func (l *Logger) Debugf(format string, args ...any) {
	l.printf(Debug, "DEBUG", format, args...)
}

func (l *Logger) Infof(format string, args ...any) {
	l.printf(Info, "INFO", format, args...)
}

func (l *Logger) Warnf(format string, args ...any) {
	l.printf(Warn, "WARN", format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	l.printf(Error, "ERROR", format, args...)
}

func (l *Logger) printf(level Level, label string, format string, args ...any) {
	if l == nil || level < l.min {
		return
	}
	l.log.Printf(label+" "+format, args...)
}
