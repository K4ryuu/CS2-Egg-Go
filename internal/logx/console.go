// SPDX-License-Identifier: GPL-3.0-or-later

// Package logx is the egg's console voice: the "PREFIX | LEVEL | message"
// table format the panel shows, the KL-XXX-NN coded messages, the level
// gate, and the optional daily log file.
package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level is a message class. Priority decides visibility, not the order here.
type Level int

const (
	Debug Level = iota
	Info
	Success
	Running
	Warning
	Guard // security events: rule trips and node blocks
	Input // a command typed into the panel console
	Error
)

const (
	red     = "\033[0;31m"
	green   = "\033[0;32m"
	yellow  = "\033[0;33m"
	blue    = "\033[0;34m"
	magenta = "\033[0;35m"
	cyan    = "\033[0;36m"
	gray    = "\033[0;90m"
	nc      = "\033[0m"
)

// DocsURL is appended to every coded message as "Docs: <url>#<code>".
var DocsURL = "https://github.com/K4ryuu/CS2-Egg-Go/blob/main/docs/reference/error-codes.md"

var levels = [...]struct {
	name, tag, color string
	priority         int
}{
	Debug:   {"debug", "DEBUG", gray, 0},
	Info:    {"info", "INFO", cyan, 1},
	Success: {"success", "OK", green, 1},
	Running: {"running", "RUN", yellow, 2},
	Warning: {"warning", "WARN", yellow, 2},
	Guard:   {"guard", "GUARD", magenta, 2},
	Input:   {"input", "INPUT", blue, 1},
	Error:   {"error", "ERROR", red, 3},
}

func (l Level) String() string { return levels[l].name }

// Priority is the severity rank shared by messages and the threshold.
func (l Level) Priority() int { return levels[l].priority }

// ParseLevel maps CONSOLE_LOG_LEVEL and message type names; unknown = Info.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return Debug
	case "success", "ok":
		return Success
	case "running", "run":
		return Running
	case "warning", "warn":
		return Warning
	case "guard":
		return Guard
	case "input":
		return Input
	case "error":
		return Error
	}
	return Info
}

// Sink receives one already-leveled or "server"-tagged line at a time, the
// same shape FileSink writes to a daily file. A console can pair with any
// sink, a local file, a forwarder to the node, or both through FanOut.
type Sink interface{ Write(kind, msg string) }

// FanOut writes to every sink in order. A nil entry is skipped, so a caller
// can build one from optional sinks without filtering itself first.
type FanOut []Sink

func (f FanOut) Write(kind, msg string) {
	for _, s := range f {
		if s != nil {
			s.Write(kind, msg)
		}
	}
}

// Console writes formatted lines to Out. Safe for concurrent use.
type Console struct {
	Prefix string
	Min    Level
	Out    io.Writer
	Mask   func(string) string // secret masking, applied before output
	File   Sink                // optional daily log file, or a FanOut of several
	mu     sync.Mutex
}

// New returns a console with the given prefix and visibility threshold.
func New(prefix string, min Level, out io.Writer) *Console {
	return &Console{Prefix: prefix, Min: min, Out: out}
}

// Log prints one line when level clears the threshold.
func (c *Console) Log(level Level, msg string) {
	if level.Priority() < c.Min.Priority() {
		return
	}
	msg = strings.TrimRight(msg, " \t\r\n")
	if c.Mask != nil {
		msg = c.Mask(msg)
	}
	l := levels[level]
	sep := gray + "|" + nc
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(c.Out, "%s%s%s %s %s%-5s%s %s %s%s%s\n", red, c.Prefix, nc, sep, l.color, l.tag, nc, sep, l.color, msg, nc)
	if c.File != nil {
		c.File.Write(l.name, msg)
	}
}

// Code prints a coded message, its hints and the docs pointer.
func (c *Console) Code(level Level, code, msg string, hints ...string) {
	c.Log(level, "["+code+"] "+msg)
	for _, h := range hints {
		c.Log(level, "  → "+h)
	}
	c.Log(level, "  → Docs: "+DocsURL+"#"+strings.ToLower(code))
}

// Logf is Log with formatting.
func (c *Console) Logf(level Level, format string, args ...any) {
	c.Log(level, fmt.Sprintf(format, args...))
}

// FileSink appends "[ts] [type] message" lines to Dir/YYYY-MM-DD.log.
type FileSink struct {
	Dir string
	Now func() time.Time
}

// Write appends one "[ts] [kind] msg" line. kind is a level name for the
// egg's own messages, or "server" for a passthrough game console line.
func (f *FileSink) Write(kind, msg string) {
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(f.Dir, now.Format("2006-01-02")+".log")
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	fmt.Fprintf(fh, "[%s] [%s] %s\n", now.Format("2006-01-02 15:04:05"), kind, msg)
}
