// SPDX-License-Identifier: GPL-3.0-or-later

package ui_test

import (
	"bytes"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

func TestTableAlignsColouredCells(t *testing.T) {
	ui.Color = true
	var buf bytes.Buffer
	ui.Table(&buf, []string{"ip", "ban", "why"}, [][]string{
		{ui.Bold("1.2.3.4"), "30m", "stray_no_connection, abc"},
		{"2001:db8::1", ui.Yellow("1440m"), "manual"},
	})
	lines := strings.Split(strings.TrimSuffix(plain(buf.String()), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("header, rule, 2 rows: %q", lines)
	}
	col := strings.Index(lines[0], "ban")
	if strings.Index(lines[2], "30m") != col || strings.Index(lines[3], "1440m") != col {
		t.Fatalf("ban column misaligned:\n%s", plain(buf.String()))
	}
	if !strings.HasPrefix(lines[1], "  ─") {
		t.Fatalf("rule line: %q", lines[1])
	}
}

func TestNoColorLeavesPlainText(t *testing.T) {
	ui.Color = false
	var buf bytes.Buffer
	ui.Ok(&buf, "done %d", 3)
	ui.Warn(&buf, "careful")
	ui.KV(&buf, "key", "value")
	got := buf.String()
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escapes with colour off: %q", got)
	}
	if !strings.HasPrefix(got, "✓ DONE  done 3\n⚠ WARN  careful\n") || !strings.Contains(got, "  key:                   value\n") {
		t.Fatalf("format: %q", got)
	}
}

func TestDoctorReportShapeAndExitCode(t *testing.T) {
	ui.Color = false
	var buf bytes.Buffer
	code := doctor.Print(&buf, "cs2node Doctor", []doctor.Check{
		doctor.Ok("Node", "service active"),
		doctor.Warnf("Host guard", "egg not connected"),
		doctor.Failf("VPK sync", "no build"),
		doctor.Fixedf("Node", "cron recreated"),
	})
	out := buf.String()
	if code != 1 {
		t.Fatal("a FAIL must exit 1")
	}
	for _, want := range []string{"cs2node Doctor", "==> Node", "  OK     service active", "==> Host guard", "  WARN   egg not connected", "  FAIL   no build", "  FIXED  cron recreated", "1 warning(s), 1 failure(s), 1 auto-fixed", "✗ ERROR Doctor found 1 blocking issue(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	buf.Reset()
	if doctor.Print(&buf, "x", []doctor.Check{doctor.Ok("Node", "fine")}) != 0 || !strings.Contains(buf.String(), "✓ DONE  Everything looks healthy") {
		t.Fatal("all-OK report must exit 0 and say healthy")
	}
}

func TestLogHandlerVoice(t *testing.T) {
	ui.Color = false
	var buf bytes.Buffer
	log := slog.New(ui.NewLogHandler(&buf))
	log.Info("container up", "container", "abc", "ports", []uint16{27015})
	log.Warn("push failed", "err", "disk full")
	got := buf.String()
	if !strings.Contains(got, "ℹ INFO  container up  container=abc  ports=[27015]\n") || !strings.Contains(got, "⚠ WARN  push failed  err=disk full\n") {
		t.Fatalf("log lines: %q", got)
	}
}
