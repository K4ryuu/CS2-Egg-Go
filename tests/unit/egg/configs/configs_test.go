// SPDX-License-Identifier: GPL-3.0-or-later

package configs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/configs"
)

func TestFreshDirGetsAllFourFilesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	all, events, err := configs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("want 4 files, got %d", len(events))
	}
	for _, e := range events {
		if !e.Report.Created {
			t.Errorf("%s not created", e.File)
		}
		if _, err := os.Stat(filepath.Join(dir, e.File)); err != nil {
			t.Errorf("%s missing on disk", e.File)
		}
	}
	if all.Filter.Patterns[0] != "Certificate expires" || all.Filter.PreviewMode {
		t.Errorf("filter defaults: %+v", all.Filter)
	}
	if len(all.Cleanup.Rules) != 8 || all.Cleanup.Rules[7].Name != "core_dumps" || all.Cleanup.Rules[7].Hours != 0 || all.Cleanup.Rules[7].IsRecursive() {
		t.Errorf("cleanup defaults: %+v", all.Cleanup.Rules)
	}
	if !all.Cleanup.Rules[4].DeleteParentDir {
		t.Error("swiftly_crash_reports must delete parent dirs")
	}
	if all.Logging.Logging.ConsoleLevel != "INFO" || all.Logging.Logging.FileEnabled || all.Logging.Logging.MaxDays != 7 {
		t.Errorf("logging defaults: %+v", all.Logging)
	}
	if all.Guard.Action != "log" || all.Guard.BanMinutes != 30 || len(all.Guard.Rules) != 7 || all.Guard.Rules[4].BanMinutes != 1440 || !all.Guard.Rules[0].Logs() {
		t.Errorf("guard defaults: %+v", all.Guard)
	}
}

func TestUserRulesSurviveAMigration(t *testing.T) {
	dir := t.TempDir()
	old := `{"version":"1.0.0","action":"block","behavior_rules":[{"name":"connect_flood","threshold":2,"window_secs":5,"ban_minutes":10,"log":false}]}`
	if err := os.WriteFile(filepath.Join(dir, "guard.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	all, _, err := configs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if all.Guard.Action != "block" || len(all.Guard.Rules) != 7 {
		t.Fatalf("migration lost data: %+v", all.Guard)
	}
	r := all.Guard.Rules[0]
	if r.Name != "connect_flood" || r.Threshold != 2 || r.Logs() {
		t.Errorf("user rule must win by name: %+v", r)
	}
}

// A pre-1.2.4 install's PREFIX_TEXT panel variable must survive the move to
// logging.json's own prefix field, once.
func TestMigratePrefixFoldsIntoLoggingJSON(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := configs.Load(dir); err != nil {
		t.Fatal(err)
	}
	if err := configs.MigratePrefix(dir, "MyServer"); err != nil {
		t.Fatal(err)
	}
	all, _, err := configs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if all.Logging.Logging.Prefix != "MyServer" {
		t.Fatalf("migrated prefix lost: %+v", all.Logging.Logging)
	}
	if all.Logging.Logging.ConsoleLevel != "INFO" {
		t.Fatalf("migration must not disturb other logging fields: %+v", all.Logging.Logging)
	}
}
