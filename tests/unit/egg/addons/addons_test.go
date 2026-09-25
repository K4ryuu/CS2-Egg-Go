// SPDX-License-Identifier: GPL-3.0-or-later

package addons_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

func TestFetchReleaseStableAndPrereleaseShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/a/b/releases/latest":
			w.Write([]byte(`{"tag_name":"v1.0.335","prerelease":false,"assets":[{"name":"x-with-runtime-linux-x64.zip","browser_download_url":"http://dl/x.zip"}]}`))
		case "/repos/a/b/releases":
			w.Write([]byte(`[{"tag_name":"2.0.0","prerelease":true,"assets":[{"name":"mmsource-2.0.0-git1391-linux.tar.gz","browser_download_url":"http://dl/mm.tgz"},{"name":"mmsource-2.0.0-git1391-windows.zip","browser_download_url":"http://dl/mm.zip"}]}]`))
		case "/repos/limited/x/releases/latest":
			w.WriteHeader(403)
		default:
			w.WriteHeader(504)
		}
	}))
	defer srv.Close()
	addons.APIBase = srv.URL
	ctx := context.Background()

	rel, err := addons.FetchRelease(ctx, srv.Client(), "a/b", false)
	if err != nil || rel.Tag != "v1.0.335" {
		t.Fatalf("stable: %v %+v", err, rel)
	}
	a, ok := rel.Asset(regexp.MustCompile(`-with-runtime-linux-.*\.zip$`))
	if !ok || a.URL != "http://dl/x.zip" {
		t.Fatalf("asset regex: %v %+v", ok, a)
	}
	if _, ok := rel.Asset(regexp.MustCompile(`nothing`)); ok {
		t.Fatal("no match must report false")
	}

	rel, err = addons.FetchRelease(ctx, srv.Client(), "a/b", true)
	if err != nil || rel.Tag != "2.0.0" {
		t.Fatalf("prerelease array: %v %+v", err, rel)
	}
	a, _ = rel.Asset(regexp.MustCompile(`linux\.tar\.gz$`))
	if regexp.MustCompile(`git[0-9]+`).FindString(a.Name) != "git1391" {
		t.Fatalf("metamod version from asset name: %s", a.Name)
	}

	if _, err := addons.FetchRelease(ctx, srv.Client(), "limited/x", false); !errors.Is(err, addons.ErrRateLimited) {
		t.Fatalf("403 must be ErrRateLimited, got %v", err)
	}
	if _, err := addons.FetchRelease(ctx, srv.Client(), "gone/x", false); err == nil || errors.Is(err, addons.ErrRateLimited) {
		t.Fatalf("504 must be a plain error, got %v", err)
	}
}

func TestNeedsUpdateVerdicts(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        addons.Verdict
	}{
		{"", "1.0.0", addons.Install},
		{"1.0.9", "1.0.10", addons.Install},
		{"v1.0.10", "1.0.10", addons.UpToDate},
		{"1.0.10", "1.0.9", addons.Downgrade},
		{"git1400", "git1391", addons.Downgrade},
	}
	for _, c := range cases {
		if got := addons.NeedsUpdate(c.cur, c.latest); got != c.want {
			t.Errorf("NeedsUpdate(%q, %q) = %v, want %v", c.cur, c.latest, got, c.want)
		}
	}
}

func TestVersionsFileRoundTrip(t *testing.T) {
	v := addons.Versions{Path: filepath.Join(t.TempDir(), "versions.txt")}
	if v.Get("Metamod") != "" {
		t.Fatal("missing file = empty")
	}
	v.Set("Metamod", "git1391")
	v.Set("CSS", "v1.0.335")
	v.Set("Metamod", "git1400")
	if v.Get("Metamod") != "git1400" || v.Get("CSS") != "v1.0.335" {
		t.Fatalf("got %q %q", v.Get("Metamod"), v.Get("CSS"))
	}
	data, _ := os.ReadFile(v.Path)
	if strings.Count(string(data), "\n") != 2 {
		t.Fatalf("one line per key: %q", data)
	}
}

func TestDownloadTriesMirrorsAndRejectsEmpty(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.String())
		switch {
		case strings.HasSuffix(r.URL.Path, "/empty"):
			w.WriteHeader(200)
		case strings.Contains(r.URL.String(), "mirror-ok"):
			w.Write([]byte("payload"))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	addons.Mirrors = []string{srv.URL + "/mirror-fail/", srv.URL + "/mirror-ok/"}
	dest := filepath.Join(t.TempDir(), "f")
	err := addons.Download(context.Background(), srv.Client(), srv.URL+"/github.com/x/y/z.zip", dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("original + 2 mirrors in order: %v", hits)
	}
	if err := addons.Download(context.Background(), srv.Client(), srv.URL+"/empty", dest, nil); err == nil {
		t.Fatal("empty body must fail")
	}
}

func makeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	os.WriteFile(path, buf.Bytes(), 0o644)
}

func makeTgz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	os.WriteFile(path, buf.Bytes(), 0o644)
}

func TestExtractRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	z := filepath.Join(dir, "a.zip")
	makeZip(t, z, map[string]string{"addons/ok.txt": "1", "../evil": "2"})
	if err := addons.ExtractZip(z, filepath.Join(dir, "out")); err == nil {
		t.Fatal("zip slip must fail")
	}
	tg := filepath.Join(dir, "a.tgz")
	makeTgz(t, tg, map[string]string{"addons/metamod/bin/x.so": "so"})
	if err := addons.ExtractTarGz(tg, filepath.Join(dir, "out2")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "out2/addons/metamod/bin/x.so")); string(data) != "so" {
		t.Fatal("tar content missing")
	}
}

const gameinfo = `"GameInfo"
{
	FileSystem
	{
		SearchPaths
		{
			Game_LowViolence	csgo_lv // Perfect World content override
			Game	csgo
			Game	core
		}
	}
	"RequireLoginForDedicatedServers"	"1"
}
`

func TestGameinfoAddOrderAndTokenless(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gameinfo.gi")
	os.WriteFile(p, []byte(gameinfo), 0o644)
	gi := addons.Gameinfo{Path: p}

	if changed, err := gi.Add("csgo/addons/swiftlys2"); err != nil || !changed {
		t.Fatalf("add swiftly: %v %v", changed, err)
	}
	if changed, err := gi.Add("csgo/addons/metamod"); err != nil || !changed {
		t.Fatalf("add metamod: %v %v", changed, err)
	}
	if changed, _ := gi.Add("csgo/addons/metamod"); changed {
		t.Fatal("second add must be a no-op")
	}
	data, _ := os.ReadFile(p)
	lines := strings.Split(string(data), "\n")
	// metamod was added last, so it sits right under LowViolence already
	if !strings.Contains(lines[7], "Game    csgo/addons/metamod") || !strings.Contains(lines[8], "Game    csgo/addons/swiftlys2") {
		t.Fatalf("insertion order: %q", lines[6:9])
	}
	if changed, _ := gi.MetamodFirst(); changed {
		t.Fatal("already first, no reorder")
	}

	// put swiftly before metamod and check the reorder
	swapped := strings.Replace(string(data), lines[7]+"\n"+lines[8], lines[8]+"\n"+lines[7], 1)
	os.WriteFile(p, []byte(swapped), 0o644)
	if changed, err := gi.MetamodFirst(); err != nil || !changed {
		t.Fatalf("reorder: %v %v", changed, err)
	}
	data, _ = os.ReadFile(p)
	lines = strings.Split(string(data), "\n")
	if !strings.Contains(lines[7], "csgo/addons/metamod") || !strings.Contains(lines[8], "csgo/addons/swiftlys2") || strings.Count(string(data), "metamod") != 1 {
		t.Fatalf("after reorder: %q", lines[6:9])
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("backup must be removed after a verified edit")
	}

	if changed, err := gi.SetTokenless(true); err != nil || !changed {
		t.Fatalf("tokenless: %v %v", changed, err)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), `"RequireLoginForDedicatedServers"	"0"`) {
		t.Fatalf("tokenless value: %s", data)
	}
	if changed, _ := gi.SetTokenless(true); changed {
		t.Fatal("already set")
	}
	if gi.HasSharp() {
		t.Fatal("no sharp yet")
	}
	gi.Add("sharp")
	if !gi.HasSharp() {
		t.Fatal("sharp detection")
	}
}

func TestMetamodUpdaterEndToEnd(t *testing.T) {
	root := t.TempDir()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/alliedmodders/metamod-source/releases":
			w.Write([]byte(`[{"tag_name":"2.0.0","prerelease":true,"assets":[{"name":"mmsource-2.0.0-git1391-linux.tar.gz","browser_download_url":"` + addons.APIBase + `/dl/mm.tgz"}]}]`))
		case r.URL.Path == "/dl/mm.tgz":
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			body := "so"
			tw.WriteHeader(&tar.Header{Name: "addons/metamod/bin/server.so", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
			tw.Write([]byte(body))
			tw.Close()
			gz.Close()
			w.Write(buf.Bytes())
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	addons.APIBase = api.URL
	addons.Mirrors = nil
	os.MkdirAll(filepath.Join(root, "game/csgo"), 0o755)
	os.WriteFile(filepath.Join(root, "game/csgo/gameinfo.gi"), []byte(gameinfo), 0o644)
	env := &addons.Env{
		Root: root, Temp: filepath.Join(root, "temps"),
		Versions: addons.Versions{Path: filepath.Join(root, "versions.txt")},
		Log:      logx.New("T", logx.Debug, &bytes.Buffer{}), HTTP: api.Client(),
	}
	env.UpdateAll(context.Background(), addons.Selection{Metamod: true}, false)
	if _, err := os.Stat(filepath.Join(root, "game/csgo/addons/metamod/bin/server.so")); err != nil {
		t.Fatal("metamod not installed")
	}
	if env.Versions.Get("Metamod") != "git1391" {
		t.Fatalf("version not recorded: %q", env.Versions.Get("Metamod"))
	}
	gi, _ := os.ReadFile(filepath.Join(root, "game/csgo/gameinfo.gi"))
	if !strings.Contains(string(gi), "Game    csgo/addons/metamod") {
		t.Fatal("gameinfo not patched")
	}
	if _, err := os.Stat(filepath.Join(root, "game/csgo/backups")); err != nil {
		t.Fatal("backups directory not created")
	}
	if backups, mm := strings.Index(string(gi), "Game    csgo/backups"), strings.Index(string(gi), "Game    csgo/addons/metamod"); backups < 0 || mm < 0 || backups > mm {
		t.Fatalf("backups must sit ahead of metamod in the search order: %s", gi)
	}
	if _, err := os.Stat(filepath.Join(root, "temps")); err == nil {
		t.Fatal("temp dir must be removed")
	}
	var out bytes.Buffer
	env.Log = logx.New("T", logx.Debug, &out)
	env.UpdateAll(context.Background(), addons.Selection{Metamod: true}, false)
	if !strings.Contains(out.String(), "Metamod is up-to-date (git1391)") {
		t.Fatalf("second run must be up-to-date: %s", out.String())
	}
}

// filepath.Join rewrites an absolute symlink target to sit under the target
// dir, so the old lexical check passed it while os.Symlink stored it
// verbatim. A later entry then wrote straight through the link.
func TestExtractRefusesASymlinkOutOfTheTree(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "autoexec.cfg"), []byte("original"), 0o644)

	tg := filepath.Join(dir, "evil.tgz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// a real archive carries its directories, which is also what makes the
	// target dir exist before the link entry lands
	tw.WriteHeader(&tar.Header{Name: "addons", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "addons/cfg", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0o777})
	body := "exec evil"
	tw.WriteHeader(&tar.Header{Name: "addons/cfg/autoexec.cfg", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	os.WriteFile(tg, buf.Bytes(), 0o644)

	addons.ExtractTarGz(tg, filepath.Join(dir, "out"))
	if got, _ := os.ReadFile(filepath.Join(outside, "autoexec.cfg")); string(got) != "original" {
		t.Fatalf("an archive wrote through a symlink out of the target dir: %q", got)
	}
}

// A mirror serves native code that the next server start loads, and there
// is nothing to verify it against, so the fallback is off unless an
// operator turns it on knowingly.
func TestMirrorsAreOffByDefault(t *testing.T) {
	t.Setenv("ADDON_MIRRORS", "")
	if got := addons.MirrorsFor(""); len(got) != 0 {
		t.Fatalf("mirrors must be empty by default, got %v", got)
	}
	if got := addons.MirrorsFor("https://mirror.example/, http://nope/, junk"); len(got) != 1 || got[0] != "https://mirror.example/" {
		t.Fatalf("only https prefixes are taken: %v", got)
	}
}
