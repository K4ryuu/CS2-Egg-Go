// SPDX-License-Identifier: GPL-3.0-or-later

package maintenance_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/maintenance"
)

type fakeEngine struct{ pruned, cached int }

func (f *fakeEngine) Dangling(context.Context) (int, int64, error) { return 3, 300, nil }
func (f *fakeEngine) PruneImages(context.Context) (int, int64, error) {
	f.pruned++
	return 2, 200, nil
}
func (f *fakeEngine) PruneBuildCache(context.Context, time.Duration) (int, int64, error) {
	f.cached++
	return 1, 100, nil
}

// journal fake: usage shrinks after a vacuum
type fakeExec struct {
	calls   []string
	vacuumd bool
}

func (f *fakeExec) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "journalctl" && len(args) > 0 && strings.HasPrefix(args[0], "--vacuum-time") {
		f.vacuumd = true
		return nil, nil
	}
	if name == "journalctl" {
		if f.vacuumd {
			return []byte("Archived and active journals take up 100.0M in the file system."), nil
		}
		return []byte("Archived and active journals take up 1.0G in the file system."), nil
	}
	return nil, errors.New("unexpected " + name)
}

func newModule(t *testing.T, cfg maintenance.Config) (*maintenance.Module, *fakeEngine, *fakeExec) {
	t.Helper()
	maintenance.StateDir = t.TempDir()
	m := maintenance.New(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	eng, ex := &fakeEngine{}, &fakeExec{}
	m.Docker, m.Exec = eng, ex.run
	setCfg(m, cfg)
	return m, eng, ex
}

func TestParseJournalSize(t *testing.T) {
	for in, want := range map[string]int64{
		"Archived and active journals take up 1.0G in the file system.": 1 << 30,
		"Journals take up 512.0M in the file system.":                   512 << 20,
		"take up 4B": 4,
	} {
		got, ok := maintenance.ParseJournalSize(in)
		if !ok || got != want {
			t.Errorf("%q: got %d %v, want %d", in, got, ok, want)
		}
	}
	if _, ok := maintenance.ParseJournalSize("nothing here"); ok {
		t.Error("garbage must not parse")
	}
}

func TestDryPassDeletesNothing(t *testing.T) {
	m, eng, ex := newModule(t, maintenance.Defaults())
	res := m.Pass(context.Background(), nil, true)
	if eng.pruned+eng.cached != 0 || ex.vacuumd {
		t.Fatalf("dry pass deleted: pruned=%d cached=%d vacuumd=%v", eng.pruned, eng.cached, ex.vacuumd)
	}
	if len(res) != 3 {
		t.Fatalf("want 3 results, got %d", len(res))
	}
	if maintenance.LoadStats(maintenance.StateDir).Total() != 0 {
		t.Fatal("dry pass must not count toward the totals")
	}
}

func TestLivePassFoldsTotals(t *testing.T) {
	m, eng, _ := newModule(t, maintenance.Defaults())
	m.Pass(context.Background(), nil, false)
	if eng.pruned != 1 || eng.cached != 1 {
		t.Fatalf("pruned=%d cached=%d", eng.pruned, eng.cached)
	}
	st := maintenance.LoadStats(maintenance.StateDir)
	want := int64(200 + 100 + (1<<30 - 100<<20))
	if st.Total() != want || st.Task["images"].Items != 2 {
		t.Fatalf("total=%d want %d, tasks=%+v", st.Total(), want, st.Task)
	}
}

func TestConfigCheck(t *testing.T) {
	if err := maintenance.Defaults().Check(); err != nil {
		t.Fatal(err)
	}
	bad := maintenance.Defaults()
	bad.JournalDays = 0
	if bad.Check() == nil {
		t.Error("journal_days 0 must fail")
	}
	bad = maintenance.Defaults()
	bad.DiskWarnPercent = 95
	if bad.Check() == nil {
		t.Error("disk_warn_percent 95 must fail")
	}
}

func TestSetupConfirmAndIdempotent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "apt", "52x")
	installed := true
	ex := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dpkg-query" && installed {
			return []byte("install ok installed"), nil
		}
		return nil, nil
	}
	var out bytes.Buffer
	if err := maintenance.Setup(context.Background(), &out, strings.NewReader("n\n"), ex, file, false); err == nil {
		t.Fatal("declined prompt must cancel")
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("file written without confirmation")
	}
	if err := maintenance.Setup(context.Background(), &out, strings.NewReader(""), ex, file, true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	if string(got) != maintenance.UpgradesConf || !strings.Contains(string(got), `Automatic-Reboot "false"`) {
		t.Fatalf("bad file: %s", got)
	}
	out.Reset()
	if err := maintenance.Setup(context.Background(), &out, strings.NewReader(""), ex, file, false); err != nil || !strings.Contains(out.String(), "Already set up") {
		t.Fatalf("second run should be a no-op: %v %s", err, out.String())
	}
}

func TestSetupNeedsDebian(t *testing.T) {
	ex := func(context.Context, string, ...string) ([]byte, error) { return nil, execNotFound() }
	err := maintenance.Setup(context.Background(), &bytes.Buffer{}, strings.NewReader(""), ex, filepath.Join(t.TempDir(), "f"), true)
	if err == nil || !strings.Contains(err.Error(), "Debian") {
		t.Fatalf("got %v", err)
	}
}

// An operator's own unattended-upgrades setup must be left alone.
func TestSetupSkipsWhenAlreadyOn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "52x")
	ex := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dpkg-query":
			return []byte("install ok installed"), nil
		case "apt-config":
			return []byte("APT::Periodic::Unattended-Upgrade \"1\";\nUnattended-Upgrade::Automatic-Reboot \"true\";\n"), nil
		}
		return nil, nil
	}
	var out bytes.Buffer
	if err := maintenance.Setup(context.Background(), &out, strings.NewReader(""), ex, file, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("wrote a drop-in over an existing setup")
	}
	if !strings.Contains(out.String(), "already on") || !strings.Contains(out.String(), "true") {
		t.Fatalf("output: %s", out.String())
	}
}
