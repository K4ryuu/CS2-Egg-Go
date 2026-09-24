// SPDX-License-Identifier: GPL-3.0-or-later

package addoncache_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/addoncache"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
)

// github serves the two shapes a pin has to cope with: a tag lookup (CSS)
// and a version that only appears in an asset name (Metamod).
func github(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/"+addons.RepoCSS+"/releases/tags/v1.0.360":
			w.Write([]byte(`{"tag_name":"v1.0.360","assets":[{"name":"css-with-runtime-linux-1.0.360.zip","browser_download_url":"https://github.com/x/css.zip"}]}`))
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.Write([]byte(`{"tag_name":"v1.0.999","assets":[]}`))
		case strings.HasSuffix(r.URL.Path, "/releases"):
			w.Write([]byte(`[
				{"tag_name":"2.0.0","prerelease":true,"assets":[{"name":"mmsource-2.0.0-git1467-linux.tar.gz","browser_download_url":"https://github.com/x/mm1467.tar.gz"}]},
				{"tag_name":"2.0.0","prerelease":true,"assets":[{"name":"mmsource-2.0.0-git1450-linux.tar.gz","browser_download_url":"https://github.com/x/mm1450.tar.gz"}]}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	old := addons.APIBase
	addons.APIBase = srv.URL
	t.Cleanup(func() { addons.APIBase = old; srv.Close() })
	return srv
}

func configFile(t *testing.T, cfg addoncache.Config) string {
	t.Helper()
	node := nodeconfig.Default()
	if err := node.SetSection("addoncache", cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := nodeconfig.Save(path, node); err != nil {
		t.Fatal(err)
	}
	return path
}

func readBack(t *testing.T, path string) addoncache.Config {
	t.Helper()
	node, err := nodeconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg addoncache.Config
	if _, err := node.Section("addoncache", &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPinResolvesByTagAndByAssetName(t *testing.T) {
	srv := github(t)
	c := addoncache.Open(t.TempDir(), srv.Client(), 0)

	css, err := c.ReleaseByVersion(context.Background(), addons.RepoCSS, "v1.0.360")
	if err != nil || css.Tag != "v1.0.360" || len(css.Assets) != 1 {
		t.Fatalf("tag lookup: %v %+v", err, css)
	}
	// Metamod tags every CS2 build "2.0.0"; the build number is in the asset
	mm, err := c.ReleaseByVersion(context.Background(), addons.RepoMetamod, "git1450")
	if err != nil || len(mm.Assets) != 1 || !strings.Contains(mm.Assets[0].Name, "git1450") {
		t.Fatalf("asset lookup: %v %+v", err, mm)
	}
	if _, err := c.ReleaseByVersion(context.Background(), addons.RepoMetamod, "git9999"); err == nil {
		t.Fatal("a version nobody published must fail, not silently pick the newest")
	}
}

func TestPinPrecedenceGlobalPerServerAndOff(t *testing.T) {
	cfg := addoncache.Config{
		Enabled: true, Dir: "/var/lib/cs2node/addoncache", RefreshMinutes: 60,
		PinVersions: true, PinMetamod: "git1450", PinCSS: "v1.0.360",
		PinPerServer: map[string]map[string]string{
			"srv-b": {"pin_metamod": "git1400"},
			"srv-c": {"pin_metamod": ""}, // this one keeps following the newest
		},
	}
	if got := cfg.Pin("srv-a", addons.RepoMetamod); got != "git1450" {
		t.Fatalf("global pin: %q", got)
	}
	if got := cfg.Pin("srv-b", addons.RepoMetamod); got != "git1400" {
		t.Fatalf("per-server pin wins: %q", got)
	}
	if got := cfg.Pin("srv-c", addons.RepoMetamod); got != "" {
		t.Fatalf("an empty per-server entry frees that server: %q", got)
	}
	if got := cfg.Pin("srv-b", addons.RepoCSS); got != "v1.0.360" {
		t.Fatalf("an unrelated per-server pin must not hide the global one: %q", got)
	}
	if got := cfg.Pin("srv-a", "some/other-repo"); got != "" {
		t.Fatalf("only the four addons are pinnable: %q", got)
	}
	off := cfg
	off.PinVersions = false
	if got := off.Pin("srv-a", addons.RepoMetamod); got != "" {
		t.Fatalf("lock off = everyone follows the newest, pins kept in the file: %q", got)
	}
	if err := (addoncache.Config{Dir: "/x", RefreshMinutes: 1, PinPerServer: map[string]map[string]string{"s": {"pin_nope": "1"}}}).Check(); err == nil {
		t.Fatal("an unknown addon in pin_per_server must be refused")
	}
}

func TestPinCommandsWriteTheConfig(t *testing.T) {
	srv := github(t)
	path := configFile(t, addoncache.Config{Enabled: true, Dir: t.TempDir(), RefreshMinutes: 60})
	ctx := context.Background()
	out := io.Discard

	if err := addoncache.SetPin(ctx, out, path, "css", "v1.0.360", "", srv.Client()); err != nil {
		t.Fatal(err)
	}
	cfg := readBack(t, path)
	if !cfg.PinVersions || cfg.PinCSS != "v1.0.360" {
		t.Fatalf("global pin not written: %+v", cfg)
	}
	// a typo must change nothing
	if err := addoncache.SetPin(ctx, out, path, "css", "v9.9.9", "", srv.Client()); err == nil {
		t.Fatal("a version that does not exist must be refused")
	}
	if readBack(t, path).PinCSS != "v1.0.360" {
		t.Fatal("a refused pin must leave the config alone")
	}
	if err := addoncache.SetPin(ctx, out, path, "nope", "v1", "", srv.Client()); err == nil {
		t.Fatal("unknown addon")
	}

	// one server on an older build, another freed from the global pin
	if err := addoncache.SetPin(ctx, out, path, "metamod", "git1450", "", srv.Client()); err != nil {
		t.Fatal(err)
	}
	if err := addoncache.SetPin(ctx, out, path, "metamod", "git1450", "srv-b", srv.Client()); err != nil {
		t.Fatal(err)
	}
	if err := addoncache.SetPin(ctx, out, path, "metamod", "latest", "srv-c", srv.Client()); err != nil {
		t.Fatal(err)
	}
	cfg = readBack(t, path)
	if cfg.PinPerServer["srv-b"]["pin_metamod"] != "git1450" || cfg.PinPerServer["srv-c"]["pin_metamod"] != "" {
		t.Fatalf("per-server pins: %+v", cfg.PinPerServer)
	}
	if cfg.Pin("srv-c", addons.RepoMetamod) != "" || cfg.Pin("srv-b", addons.RepoMetamod) != "git1450" {
		t.Fatal("the written config must behave like the in-memory one")
	}

	if err := addoncache.ClearPin(out, path, "metamod", "srv-b"); err != nil {
		t.Fatal(err)
	}
	if _, still := readBack(t, path).PinPerServer["srv-b"]; still {
		t.Fatal("an emptied server entry goes away")
	}
	if err := addoncache.ClearPin(out, path, "all", ""); err != nil {
		t.Fatal(err)
	}
	cfg = readBack(t, path)
	if cfg.PinVersions || cfg.PinCSS != "" || cfg.PinMetamod != "" || len(cfg.PinPerServer) != 0 {
		t.Fatalf("unpin all must clear everything: %+v", cfg)
	}
}

func TestPinsListing(t *testing.T) {
	path := configFile(t, addoncache.Config{
		Enabled: true, Dir: "/var/lib/cs2node/addoncache", RefreshMinutes: 60,
		PinVersions: true, PinMetamod: "git1450",
		PinPerServer: map[string]map[string]string{"srv-b": {"pin_metamod": ""}},
	})
	var b strings.Builder
	if err := addoncache.PrintPins(&b, path); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{"metamod", "git1450", "srv-b", "frees this server", "newest"} {
		if !strings.Contains(got, want) {
			t.Fatalf("listing misses %q:\n%s", want, got)
		}
	}
}

func TestModuleConfigSurvivesAWizardRoundTrip(t *testing.T) {
	// the wizard stores answers as a plain map, so the keys must match the
	// struct tags or a pin typed in the installer would vanish
	section := map[string]any{"enabled": true, "dir": "/var/lib/cs2node/addoncache", "refresh_minutes": 60, "keep_days": 30,
		"pin_versions": true, "pin_metamod": "git1450", "pin_css": "", "pin_swiftly": "", "pin_modsharp": ""}
	raw, err := json.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	var cfg addoncache.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.PinVersions || cfg.Pin("any", addons.RepoMetamod) != "git1450" {
		t.Fatalf("wizard keys: %+v", cfg)
	}
}
