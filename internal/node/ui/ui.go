// SPDX-License-Identifier: GPL-3.0-or-later

// Package ui is the look of every cs2node command: the colours, the ✓/ℹ/⚠/✗
// lines, headlines, sections, key/value blocks and aligned tables the bash
// tools had.
package ui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Color is on for a terminal without NO_COLOR. Journal output keeps colours
// too: journalctl renders them.
var Color = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if _, err := unix.IoctlGetTermios(int(os.Stdout.Fd()), termiosReq); err == nil {
		return true
	}
	// systemd's journal: no tty, but colours survive
	return os.Getenv("JOURNAL_STREAM") != "" || os.Getenv("INVOCATION_ID") != ""
}

func c(code string) string {
	if !Color {
		return ""
	}
	return "\033[" + code + "m"
}

// Styles.
func Bold(s string) string    { return c("1") + s + c("0") }
func Dim(s string) string     { return c("2") + s + c("0") }
func Red(s string) string     { return c("31") + s + c("0") }
func Green(s string) string   { return c("32") + s + c("0") }
func Yellow(s string) string  { return c("33") + s + c("0") }
func Blue(s string) string    { return c("34") + s + c("0") }
func Magenta(s string) string { return c("35") + s + c("0") }
func Cyan(s string) string    { return c("36") + s + c("0") }
func Gray(s string) string    { return c("90") + s + c("0") }

// Headline is the boxed title of a command.
func Headline(w io.Writer, title string) {
	rule := strings.Repeat("─", 54)
	fmt.Fprintln(w, Bold(Blue(rule)))
	fmt.Fprintln(w, Bold(Blue(" "+title)))
	fmt.Fprintln(w, Bold(Blue(rule)))
	fmt.Fprintln(w)
}

// Section is the "==> Title" divider of the daemon tools.
func Section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s %s\n\n", Bold(Magenta("==>")), Bold(title))
}

// Rule is the "── Title ────" divider of the installer.
func Rule(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s %s\n", Bold(Blue("── "+title)), Gray(strings.Repeat("─", 40)))
}

// Status lines.
func Info(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "ℹ %s  %s\n", Bold(Cyan("INFO")), fmt.Sprintf(format, a...))
}
func Ok(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "✓ %s  %s\n", Bold(Green("DONE")), fmt.Sprintf(format, a...))
}
func Warn(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "⚠ %s  %s\n", Bold(Yellow("WARN")), fmt.Sprintf(format, a...))
}
func Error(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "✗ %s %s\n", Bold(Red("ERROR")), fmt.Sprintf(format, a...))
}

// Step lines, the installer's quieter voice.
func Step(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "  %s  %s\n", Cyan("→"), fmt.Sprintf(format, a...))
}
func StepOk(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "  %s  %s\n", Green("✓"), fmt.Sprintf(format, a...))
}
func StepWarn(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "  %s  %s\n", Yellow("!"), fmt.Sprintf(format, a...))
}
func StepErr(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "  %s  %s\n", Red("✗"), fmt.Sprintf(format, a...))
}

// KV prints an aligned "key   value" line, the key in gray.
func KV(w io.Writer, key, value string) {
	fmt.Fprintf(w, "  %s %s\n", Gray(pad(key+":", 22)), value)
}

// KVC is KV with a cyan key, for config values.
func KVC(w io.Writer, key, value string) {
	if value == "" {
		value = Gray("<empty>")
	}
	fmt.Fprintf(w, "    %s %s\n", Cyan(pad(key, 24)), value)
}

// ServerIDs is the one-liner every command that takes a <server> prints:
// the id is the container name, which cs2node status lists.
const ServerIDs = "A <server> is the short id in the first column, or any prefix of it; cs2node status lists them."

// Line prints an indented plain line.
func Line(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "  %s\n", fmt.Sprintf(format, a...))
}

// Table prints aligned columns with a bold header and a dim rule. Cells
// may carry colour codes; widths are measured without them.
func Table(w io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = visible(h)
	}
	for _, r := range rows {
		for i, cell := range r {
			if i < len(widths) && visible(cell) > widths[i] {
				widths[i] = visible(cell)
			}
		}
	}
	line := func(cells []string, style func(string) string) {
		var b strings.Builder
		b.WriteString("  ")
		for i, cell := range cells {
			if i >= len(widths) {
				break
			}
			text := cell
			if style != nil {
				text = style(cell)
			}
			b.WriteString(text)
			if i < len(widths)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visible(cell)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(headers, Bold)
	total := 0
	for _, wd := range widths {
		total += wd + 2
	}
	fmt.Fprintln(w, "  "+Dim(strings.Repeat("─", total-2)))
	for _, r := range rows {
		line(r, nil)
	}
}

// Plain strips terminal control bytes from text that came from a server
// console (escape sequences could restyle or spoof the admin's terminal).
func Plain(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			b.WriteRune('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Ago renders how long ago t was, coarse on purpose.
func Ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// ServerID is the short form of a panel id, the first segment of the uuid.
// Commands take it too, so what is printed can be pasted back.
func ServerID(name string) string {
	if i := strings.IndexByte(name, '-'); i >= 8 {
		return name[:i]
	}
	if len(name) > 12 {
		return name[:12]
	}
	return name
}

// Or returns fallback when s carries nothing worth printing. JSON round
// trips turn a missing field into the string "<nil>", which is how a
// Discord embed once shipped that as the map name.
func Or(s, fallback string) string {
	if s == "" || s == "<nil>" {
		return fallback
	}
	return s
}

// Size renders bytes as KiB / MiB / GiB.
func Size(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%.0f KiB", float64(b)/1024)
}

func pad(s string, n int) string {
	if v := visible(s); v < n {
		return s + strings.Repeat(" ", n-v)
	}
	return s
}

// visible is the rune count without ANSI escapes.
func visible(s string) int {
	n := 0
	in := false
	for _, r := range s {
		switch {
		case in:
			if r == 'm' {
				in = false
			}
		case r == '\033':
			in = true
		default:
			n++
		}
	}
	return n
}

// LogHandler is a slog.Handler in the same voice for the daemon's journal.
type LogHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	attrs []slog.Attr
}

// NewLogHandler writes "ℹ INFO  msg  key=value" lines.
func NewLogHandler(w io.Writer) *LogHandler {
	return &LogHandler{w: w, mu: &sync.Mutex{}}
}

func (h *LogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *LogHandler) Handle(_ context.Context, r slog.Record) error {
	var tag string
	switch {
	case r.Level >= slog.LevelError:
		tag = "✗ " + Bold(Red("ERROR"))
	case r.Level >= slog.LevelWarn:
		tag = "⚠ " + Bold(Yellow("WARN")) + " "
	default:
		tag = "ℹ " + Bold(Cyan("INFO")) + " "
	}
	var b strings.Builder
	b.WriteString(tag)
	b.WriteString(" ")
	b.WriteString(r.Message)
	write := func(a slog.Attr) {
		b.WriteString("  ")
		b.WriteString(Gray(a.Key + "="))
		b.WriteString(fmt.Sprint(a.Value.Any()))
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(func(a slog.Attr) bool { write(a); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintln(h.w, b.String())
	return err
}

func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHandler{w: h.w, mu: h.mu, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

func (h *LogHandler) WithGroup(string) slog.Handler { return h }
