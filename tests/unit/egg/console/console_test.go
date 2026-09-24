// SPDX-License-Identifier: GPL-3.0-or-later

package console_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/console"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

func pipeline(patterns []string, preview bool) (*console.Pipeline, *bytes.Buffer) {
	var out bytes.Buffer
	p := &console.Pipeline{
		Log:     logx.New("P", logx.Debug, &out),
		Out:     &out,
		Mask:    console.Masker("TOKEN123"),
		Preview: preview,
	}
	if patterns != nil {
		p.Filter = console.NewFilter(patterns)
	}
	return p, &out
}

func TestMaskingAlwaysOnAndEmptyLinesPass(t *testing.T) {
	p, out := pipeline(nil, false)
	p.Handle("sv_setsteamaccount TOKEN123 ok")
	p.Handle("")
	if got := out.String(); got != "sv_setsteamaccount ******** ok\n\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFilterExactAndContainsWithPreview(t *testing.T) {
	p, out := pipeline([]string{"@Server is hibernating", "Certificate expires", ""}, true)
	e, c := p.Filter.Counts()
	if e != 1 || c != 1 {
		t.Fatalf("counts %d %d", e, c)
	}
	p.Handle("Server is hibernating")
	p.Handle("Server is hibernating now")
	p.Handle("Your Certificate expires soon")
	p.Handle("fine")
	got := out.String()
	for _, raw := range []string{"Server is hibernating", "Your Certificate expires soon"} {
		if strings.Contains(got, "\n"+raw+"\n") || strings.HasPrefix(got, raw+"\n") {
			t.Fatalf("blocked line printed raw: %q", got)
		}
	}
	if !strings.Contains(got, "fine\n") {
		t.Fatalf("filter wrong: %q", got)
	}
	if !strings.Contains(got, "Server is hibernating now\n") {
		t.Fatal("exact pattern must not match a longer line")
	}
	if strings.Count(got, "Blocked message: ") != 2 {
		t.Fatalf("preview must show both blocked lines: %q", got)
	}
	p2, out2 := pipeline([]string{"Certificate expires"}, false)
	p2.Handle("Certificate expires")
	if out2.Len() != 0 {
		t.Fatal("no preview = silent drop")
	}
}

func TestAbortAfterAStopRequestIsNotACrash(t *testing.T) {
	p, out := pipeline(nil, false)
	stopping := false
	p.Stopping = func() bool { return stopping }
	line := "./game/cs2.sh: line 109:    21 Aborted                 (core dumped) ${STEAM_RUNTIME_PREFIX} ${GAME_DEBUGGER} \"${GAMEROOT}\"/${GAMEEXE} \"$@\""
	p.Handle(line)
	if !strings.Contains(out.String(), "[KL-SRV-01]") {
		t.Fatal("an abort while running is a crash")
	}
	out.Reset()
	stopping = true
	p.Handle(line)
	p.Handle("./game/cs2.sh: line 109:    21 Segmentation fault      (core dumped) ${GAME_DEBUGGER} \"${GAMEROOT}\"/${GAMEEXE}")
	got := out.String()
	if strings.Contains(got, "[KL-SRV-01]") {
		t.Fatalf("a stop is not a crash: %q", got)
	}
	// nothing reaches the console raw: both lines are DEBUG notes, which
	// the default level hides anyway
	for _, l := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.Contains(l, "Engine exited noisily on shutdown") {
			t.Fatalf("the engine's noisy exit must stay out of the panel console: %q", l)
		}
	}
	// the engine's own log lines keep flowing while it shuts down
	out.Reset()
	p.Handle("Forgot to remove resource type manager for type vsnap!")
	if !strings.Contains(out.String(), "Forgot to remove resource type manager") {
		t.Fatalf("a shutdown must not swallow the game's own log: %q", out.String())
	}
}

func TestCrashAndGSLTAnnotations(t *testing.T) {
	p, out := pipeline(nil, false)
	p.Handle("./game/cs2.sh: line 110: 42 Aborted (core dumped) ${GAME_DEBUGGER} \"${GAMEROOT}\"/${GAMEEXE}")
	p.Handle("Segmentation fault (core dumped)")
	p.Handle("Cert request for invalid failed")
	got := out.String()
	if strings.Count(got, "[KL-SRV-01] Server crash detected") != 2 {
		t.Fatalf("crash annotation: %q", got)
	}
	if !strings.Contains(got, "[KL-SRV-02] Steam GSLT token invalid or expired") || !strings.Contains(got, "Cert request for invalid failed\n") {
		t.Fatalf("gslt annotation: %q", got)
	}
	if !strings.Contains(got, "Docs: "+logx.DocsURL+"#kl-srv-02") {
		t.Fatal("docs link missing")
	}
}

// The file the console tab writes to must hold exactly what the tab shows:
// the game server's own console output, not just the egg's internal
// messages, masked and filtered the same way, and not the lines a filter
// hid from the panel.
func TestFileSinkCapturesGameConsoleOutputToo(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	log := logx.New("P", logx.Debug, &out)
	log.File = &logx.FileSink{Dir: dir}
	p := &console.Pipeline{
		Log:    log,
		Out:    &out,
		Mask:   console.Masker("TOKEN123"),
		Filter: console.NewFilter([]string{"Certificate expires"}),
	}
	p.Handle("Map: de_dust2")
	p.Handle("sv_setsteamaccount TOKEN123 ok")
	p.Handle("Your Certificate expires soon")
	log.Log(logx.Info, "Starting server")

	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) != 1 {
		t.Fatalf("want one daily log file, got %v", files)
	}
	data, _ := os.ReadFile(files[0])
	got := string(data)
	if !strings.Contains(got, "[server] Map: de_dust2\n") {
		t.Fatalf("game console line missing from file: %q", got)
	}
	if !strings.Contains(got, "[server] sv_setsteamaccount ******** ok\n") {
		t.Fatalf("game console line in file must be masked: %q", got)
	}
	if strings.Contains(got, "Certificate expires") {
		t.Fatalf("a filtered-out line must not reach the file either: %q", got)
	}
	if !strings.Contains(got, "[info] Starting server\n") {
		t.Fatalf("the egg's own lines must still be in the same file: %q", got)
	}
}

// The server's terminal echoes whatever is typed into it, so a secret set
// from the panel comes back as an output line, lands in the crash ring and
// leaves the container in the next crash bundle.
func TestSecretsInTypedCommandsAreRedacted(t *testing.T) {
	mask := console.Masker("GSLT-TOKEN-HERE")
	cases := map[string]string{
		"rcon_password hunter2":                           "rcon_password ********",
		`sv_password "let me in"`:                         `sv_password ********`,
		"sv_setsteamaccount ABCDEF0123456789":             "sv_setsteamaccount ********",
		"Server logging data to file logs/L000_000.log":   "Server logging data to file logs/L000_000.log",
		"the token GSLT-TOKEN-HERE appears in a log line": "the token *************** appears in a log line",
	}
	for in, want := range cases {
		if got := mask(in); got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}
