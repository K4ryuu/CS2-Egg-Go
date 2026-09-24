// SPDX-License-Identifier: GPL-3.0-or-later

package logx_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

const (
	red   = "\x1b[0;31m"
	green = "\x1b[0;32m"
	cyan  = "\x1b[0;36m"
	gray  = "\x1b[0;90m"
	nc    = "\x1b[0m"
	sep   = gray + "|" + nc
)

func TestConsoleLineIsByteIdenticalToTheBashFormat(t *testing.T) {
	var buf bytes.Buffer
	c := logx.New("KitsuneLab", logx.Info, &buf)
	c.Log(logx.Info, "hello ")
	want := red + "KitsuneLab" + nc + " " + sep + " " + cyan + "INFO " + nc + " " + sep + " " + cyan + "hello" + nc + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestLevelTagsPaddedToFive(t *testing.T) {
	cases := map[logx.Level]string{
		logx.Debug: "DEBUG", logx.Info: "INFO ", logx.Success: "OK   ", logx.Running: "RUN  ",
		logx.Warning: "WARN ", logx.Guard: "GUARD", logx.Error: "ERROR",
	}
	for lvl, tag := range cases {
		var buf bytes.Buffer
		logx.New("P", logx.Debug, &buf).Log(lvl, "x")
		if !strings.Contains(buf.String(), tag+nc) {
			t.Errorf("level %v: tag %q missing in %q", lvl, tag, buf.String())
		}
	}
}

func TestThresholdGate(t *testing.T) {
	var buf bytes.Buffer
	c := logx.New("P", logx.ParseLevel("WARN"), &buf)
	c.Log(logx.Info, "hidden")
	c.Log(logx.Success, "hidden")
	c.Log(logx.Debug, "hidden")
	c.Log(logx.Running, "shown")
	c.Log(logx.Guard, "shown")
	c.Log(logx.Warning, "shown")
	c.Log(logx.Error, "shown")
	if strings.Contains(buf.String(), "hidden") || strings.Count(buf.String(), "shown") != 4 {
		t.Fatalf("gate wrong: %q", buf.String())
	}
	if logx.ParseLevel("nonsense") != logx.Info || logx.ParseLevel("warning") != logx.Warning || logx.ParseLevel("error") != logx.Error {
		t.Fatal("ParseLevel mapping")
	}
}

func TestCodedMessageHasHintsAndDocsLink(t *testing.T) {
	var buf bytes.Buffer
	c := logx.New("P", logx.Debug, &buf)
	c.Code(logx.Warning, "KL-GRD-03", "channel unverified", "check the FIFO")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "[KL-GRD-03] channel unverified") {
		t.Errorf("code line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "  → check the FIFO") {
		t.Errorf("hint line: %q", lines[1])
	}
	if !strings.Contains(lines[2], "  → Docs: "+logx.DocsURL+"#kl-grd-03") {
		t.Errorf("docs line: %q", lines[2])
	}
}

func TestMaskHookAndFileSink(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	c := logx.New("P", logx.Debug, &buf)
	c.Mask = func(s string) string { return strings.ReplaceAll(s, "SECRET", "******") }
	c.File = &logx.FileSink{Dir: dir}
	c.Log(logx.Error, "token SECRET leaked")
	if strings.Contains(buf.String(), "SECRET") {
		t.Fatal("console not masked")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) != 1 {
		t.Fatalf("want one daily log file, got %v", files)
	}
	data, _ := os.ReadFile(files[0])
	line := string(data)
	if !strings.HasPrefix(line, "[") || !strings.Contains(line, "] [error] token ****** leaked\n") {
		t.Fatalf("file line: %q", line)
	}
}

// A command typed in the panel used to come back as a bare echo: no level,
// no colour, nothing in the log file. It gets its own line now.
func TestInputLevel(t *testing.T) {
	var buf bytes.Buffer
	c := logx.New("KitsuneLab", logx.Debug, &buf)
	c.Log(logx.Input, "changelevel de_dust2")
	out := buf.String()
	if !strings.Contains(out, "INPUT") || !strings.Contains(out, "changelevel de_dust2") {
		t.Fatalf("input line: %q", out)
	}
	if logx.ParseLevel("input") != logx.Input {
		t.Fatal("the level must be selectable as CONSOLE_LOG_LEVEL")
	}
	// visible wherever info is
	if logx.Input.Priority() != logx.Info.Priority() {
		t.Fatalf("input priority %d, info %d", logx.Input.Priority(), logx.Info.Priority())
	}
}
