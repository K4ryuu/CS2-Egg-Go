// SPDX-License-Identifier: GPL-3.0-or-later

// Package console is what happens to a server output line before the panel
// sees it: crash and GSLT annotations, secret masking, the opt-in pattern
// filter.
package console

import (
	"io"
	"regexp"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

// Masker hides secret wherever it appears, keeping its length.
func Masker(secret string) func(string) string {
	stars := strings.Repeat("*", len(secret))
	return func(s string) string {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, stars)
		}
		return redactSecretCommands(s)
	}
}

// secretCommands take a secret as their argument. The server's terminal
// echoes whatever is typed into it, so an operator setting one of these in
// the panel console gets the secret back as an output line, and from there
// it reaches the crash ring and the crash bundle the node keeps on the
// host. The value is replaced everywhere the line is used.
var secretCommands = []string{
	"rcon_password",
	"sv_setsteamaccount",
	"sv_password",
	"sv_downloadurl_password",
}

// the value is either a quoted string or one bare token, and all of it goes
var reSecretCmd = regexp.MustCompile(`(?i)\b(` + strings.Join(secretCommands, "|") + `)(\s+)(?:"[^"]*"|\S+)`)

func redactSecretCommands(s string) string {
	if !strings.Contains(s, "_") {
		return s // every one of them has an underscore: cheap way out
	}
	return reSecretCmd.ReplaceAllString(s, "${1}${2}********")
}

// Filter drops lines by pattern. "@text" matches the whole line, anything
// else matches as a substring.
type Filter struct {
	exact    map[string]bool
	contains []string
}

// NewFilter compiles the patterns from console-filter.json.
func NewFilter(patterns []string) *Filter {
	f := &Filter{exact: map[string]bool{}}
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "@") {
			f.exact[p[1:]] = true
		} else {
			f.contains = append(f.contains, p)
		}
	}
	return f
}

// Counts reports the pattern totals for the startup line.
func (f *Filter) Counts() (exact, contains int) { return len(f.exact), len(f.contains) }

// Blocked reports whether line matches any pattern.
func (f *Filter) Blocked(line string) bool {
	if f.exact[line] {
		return true
	}
	for _, p := range f.contains {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

var (
	crashLine = regexp.MustCompile(`\./game/cs2\.sh:.*Aborted.*\(core dumped\)|Segmentation fault`)
)

// Pipeline is the per-line path to the panel.
type Pipeline struct {
	Log     *logx.Console
	Out     io.Writer
	Mask    func(string) string
	Filter  *Filter // nil = filtering off
	Preview bool    // re-emit blocked lines at debug
	// Stopping says a stop was requested: an abort after that is the
	// engine's exit, not a crash.
	Stopping func() bool
	// OnCrash hears about a real crash line (after it was printed).
	OnCrash func(line string)
}

// Handle prints one server line and any annotation it earns.
func (p *Pipeline) Handle(line string) {
	crash := crashLine.MatchString(line) // once: this runs on every line
	switch {
	case crash && p.Stopping != nil && p.Stopping():
		// CS2 dies with an abort or a segfault on every clean stop, so the
		// line is swallowed: printing it only makes people think it crashed
		p.Log.Logf(logx.Debug, "Engine exited noisily on shutdown (known CS2 behaviour, not a crash): %s", line)
	case crash:
		p.print(line)
		p.Log.Code(logx.Warning, "KL-SRV-01", "Server crash detected",
			"Review stack trace above for the failing module",
			"Common causes: outdated addons, plugin incompatibility, stale gamedata")
		if p.OnCrash != nil {
			p.OnCrash(line)
		}
	case strings.Contains(line, "Cert request for invalid failed") || strings.Contains(line, "We're not logged into Steam"):
		p.print(line)
		p.Log.Code(logx.Warning, "KL-SRV-02", "Steam GSLT token invalid or expired",
			"Regenerate at https://steamcommunity.com/dev/managegameservers (App ID 730)",
			"Update STEAM_ACC in panel startup variables and restart the server")
	default:
		p.print(line)
	}
}

func (p *Pipeline) print(line string) {
	if line == "" {
		io.WriteString(p.Out, "\n")
		p.toFile("")
		return
	}
	if p.Mask != nil {
		line = p.Mask(line)
	}
	if p.Filter != nil && p.Filter.Blocked(line) {
		if p.Preview {
			p.Log.Log(logx.Debug, "Blocked message: "+line)
		}
		return
	}
	io.WriteString(p.Out, line+"\n")
	p.toFile(line)
}

// toFile mirrors a line the panel console just showed into the same daily
// log file the egg's own messages go to, so the file matches the console
// tab: game output and egg lines merged, masked and filter the same way.
func (p *Pipeline) toFile(line string) {
	if p.Log != nil && p.Log.File != nil {
		p.Log.File.Write("server", line)
	}
}
