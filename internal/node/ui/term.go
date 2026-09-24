// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// TermSize is the terminal's columns and rows, 80x24 when fd is no tty.
func TermSize(fd int) (cols, rows int) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// RawMode turns off line buffering and echo on fd so single keys arrive;
// the returned func restores the terminal. A no-op off a tty.
func RawMode(fd int) func() {
	old, err := unix.IoctlGetTermios(fd, termiosReq)
	if err != nil {
		return func() {}
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, termiosSet, &raw); err != nil {
		return func() {}
	}
	return func() { unix.IoctlSetTermios(fd, termiosSet, old) }
}

// Screen switches to the alternate screen with a hidden cursor and hands
// back the func that returns to the normal one.
func Screen(w io.Writer) func() {
	fmt.Fprint(w, "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J")
	return func() { fmt.Fprint(w, "\x1b[?25h\x1b[?1049l") }
}

// Frame writes lines from the top-left, clearing what each line and the
// rest of the screen held before, without a full clear (no flicker).
func Frame(w io.Writer, lines []string) {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\x1b[K\r\n")
	}
	b.WriteString("\x1b[J")
	io.WriteString(w, b.String())
}

// Bar renders [|||||     ] of the given inner width, coloured by load.
func Bar(width int, frac float64) string {
	if width < 1 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := int(frac*float64(width) + 0.5)
	fill := strings.Repeat("|", n) + strings.Repeat(" ", width-n)
	switch {
	case frac >= 0.85:
		fill = Red(fill)
	case frac >= 0.6:
		fill = Yellow(fill)
	default:
		fill = Green(fill)
	}
	return Gray("[") + fill + Gray("]")
}

// Short renders bytes tightly for a table cell: 684M, 1.9G.
func Short(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%dB", b)
}

// Fit pads or cuts text to exactly n visible cells (escape sequences do not
// count); right aligns when right is set.
func Fit(s string, n int, right bool) string {
	if n <= 0 {
		return ""
	}
	switch w := visible(s); {
	case w > n && n > 1:
		return Clip(s, n-1) + "…"
	case w > n:
		return Clip(s, n)
	default:
		pad := strings.Repeat(" ", n-w)
		if right {
			return pad + s
		}
		return s + pad
	}
}

// Clip cuts styled text to n visible cells, keeping escape sequences
// intact and closing any open style.
func Clip(s string, n int) string {
	var b strings.Builder
	seen, i := 0, 0
	styled := false
	for i < len(s) {
		if s[i] == 0x1b {
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			esc := s[i:j]
			b.WriteString(esc)
			styled = esc != c("0")
			i = j
			continue
		}
		if seen == n {
			break
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		seen++
		i += size
	}
	if seen == n && i < len(s) && styled {
		b.WriteString(c("0"))
	}
	return b.String()
}

// Visible is the rune count without ANSI escapes.
func Visible(s string) int { return visible(s) }
