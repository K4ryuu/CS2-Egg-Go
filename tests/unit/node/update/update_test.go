// SPDX-License-Identifier: GPL-3.0-or-later

package update_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/update"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func server(t *testing.T, binary []byte, sums string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := func(tag string, pre bool) string {
			return `{"tag_name":"` + tag + `","prerelease":` + map[bool]string{true: "true", false: "false"}[pre] + `,"assets":[` +
				`{"name":"cs2node_linux_amd64","browser_download_url":"` + srv.URL + `/dl/bin"},` +
				`{"name":"checksums.txt","browser_download_url":"` + srv.URL + `/dl/sums"}]}`
		}
		switch r.URL.Path {
		case "/repos/" + update.Repo + "/releases/latest":
			w.Write([]byte(rel("v2.1.0", false)))
		case "/repos/" + update.Repo + "/releases":
			w.Write([]byte("[" + rel("v2.2.0-beta.3", true) + "," + rel("v2.1.0", false) + "]"))
		case "/dl/bin":
			w.Write(binary)
		case "/dl/sums":
			w.Write([]byte(sums))
		default:
			w.WriteHeader(404)
		}
	}))
	addons.APIBase = srv.URL
	// the updater only follows github over https; point the allowlist at
	// the test server instead of weakening the check
	old := update.ReleaseHosts
	update.ReleaseHosts = []string{"127.0.0.1"}
	t.Cleanup(func() { update.ReleaseHosts = old })
	addons.Mirrors = nil
	return srv
}

func TestCheckFollowsTheChannel(t *testing.T) {
	bin := []byte("new-binary")
	srv := server(t, bin, sum(bin)+"  cs2node_linux_amd64\n")
	defer srv.Close()
	ctx := context.Background()

	if _, err := update.Check(ctx, srv.Client(), "dev", "1.0.0"); !errors.Is(err, update.ErrUpToDate) {
		t.Fatalf("dev never updates: %v", err)
	}
	c, err := update.Check(ctx, srv.Client(), "stable", "2.0.0")
	if err != nil || c.Version != "2.1.0" {
		t.Fatalf("stable: %+v %v", c, err)
	}
	c, err = update.Check(ctx, srv.Client(), "beta", "2.1.0")
	if err != nil || c.Version != "2.2.0-beta.3" {
		t.Fatalf("beta takes prereleases: %+v %v", c, err)
	}
	if _, err := update.Check(ctx, srv.Client(), "stable", "2.1.0"); !errors.Is(err, update.ErrUpToDate) {
		t.Fatalf("same version: %v", err)
	}
	if _, err := update.Check(ctx, srv.Client(), "stable", "3.0.0"); !errors.Is(err, update.ErrUpToDate) {
		t.Fatalf("newer local build never downgrades: %v", err)
	}
	if _, err := update.Check(ctx, srv.Client(), "nightly", "1.0.0"); err == nil {
		t.Fatal("unknown channel must fail")
	}
}

func TestApplyVerifiesChecksumAndKeepsPrev(t *testing.T) {
	bin := []byte("new-binary")
	srv := server(t, bin, "deadbeef  other\n"+sum(bin)+"  cs2node_linux_amd64\n")
	defer srv.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "cs2node")
	os.WriteFile(path, []byte("old-binary"), 0o755)
	c, err := update.Check(context.Background(), srv.Client(), "stable", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := update.Apply(context.Background(), srv.Client(), c, path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new-binary" {
		t.Fatalf("binary not replaced: %q", got)
	}
	if prev, _ := os.ReadFile(path + ".prev"); string(prev) != "old-binary" {
		t.Fatal("previous binary must be kept as .prev")
	}
	if st, _ := os.Stat(path); st.Mode().Perm()&0o111 == 0 {
		t.Fatal("new binary must be executable")
	}
	if _, err := os.Stat(filepath.Join(dir, ".cs2node.sums")); err == nil {
		t.Fatal("checksum temp file must be removed")
	}
}

func TestApplyRefusesABadChecksum(t *testing.T) {
	bin := []byte("evil")
	srv := server(t, bin, sum([]byte("something else"))+"  cs2node_linux_amd64\n")
	defer srv.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "cs2node")
	os.WriteFile(path, []byte("old-binary"), 0o755)
	c, _ := update.Check(context.Background(), srv.Client(), "stable", "1.0.0")
	err := update.Apply(context.Background(), srv.Client(), c, path)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old-binary" {
		t.Fatal("binary must be untouched")
	}
	if _, err := os.Stat(filepath.Join(dir, ".cs2node.new")); err == nil {
		t.Fatal("rejected download must be removed")
	}
}

// The addon downloader falls back to community mirrors when GitHub is slow.
// That is right for a plugin zip and wrong for the daemon's own binary: a
// mirror serving both the binary and a matching checksums.txt would pass
// every check we make.
func TestOnlyGitHubOverTLS(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/K4ryuu/CS2-Egg-Go/releases/download/v1/cs2node_linux_amd64",
		"https://objects.githubusercontent.com/x",
		"https://release-assets.githubusercontent.com/x",
	} {
		if !update.FromGitHub(raw) {
			t.Errorf("%s must be allowed", raw)
		}
	}
	for _, raw := range []string{
		"https://ghproxy.net/https://github.com/x",
		"https://gh.llkk.cc/https://github.com/x",
		"http://github.com/x",
		"file:///etc/shadow",
		"https://github.com.evil.tld/x",
		"https://user:pass@github.com/x",
		"https://raw.githubusercontent.com/x",
		"",
	} {
		if update.FromGitHub(raw) {
			t.Errorf("%s must be refused", raw)
		}
	}
}

// A release whose assets point somewhere else is refused before anything
// is fetched, so the answer is a clear no rather than a failed download.
func TestCheckRefusesForeignAssets(t *testing.T) {
	srv := server(t, []byte("bin"), sum([]byte("bin"))+"  cs2node_linux_amd64\n")
	defer srv.Close()
	old := update.ReleaseHosts
	update.ReleaseHosts = []string{"github.com"} // the test server is not on it
	defer func() { update.ReleaseHosts = old }()
	if _, err := update.Check(context.Background(), srv.Client(), "stable", "1.0.0"); !errors.Is(err, update.ErrNotGitHub) {
		t.Fatalf("want ErrNotGitHub, got %v", err)
	}
}
