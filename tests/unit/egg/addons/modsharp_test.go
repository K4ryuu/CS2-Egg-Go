// SPDX-License-Identifier: GPL-3.0-or-later

package addons_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

func runtimeconfig(version string) string {
	return `{"runtimeOptions":{"tfm":"net11.0","rollForward":"LatestMinor","framework":{"name":"Microsoft.NETCore.App","version":"` + version + `"}}}`
}

// the core says which runtime it was built for, a pinned one would leave a
// newer core unable to load
func TestModSharpInstallsTheRuntimeItsCoreNames(t *testing.T) {
	cases := []struct{ name, config, want string }{
		{"named", runtimeconfig("11.0.0"), "11.0.0"},
		{"missing", "", addons.DotNetVersion},
		{"not a version", runtimeconfig("../../evil"), addons.DotNetVersion},
	}
	oldAPI, oldMirrors, oldURL := addons.APIBase, addons.Mirrors, addons.DotNetURL
	t.Cleanup(func() { addons.APIBase, addons.Mirrors, addons.DotNetURL = oldAPI, oldMirrors, oldURL })
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, files := t.TempDir(), t.TempDir()
			core := map[string]string{"sharp/core/Sharp.Core.dll": "dll"}
			if tc.config != "" {
				core["sharp/core/Sharp.Core.runtimeconfig.json"] = tc.config
			}
			makeZip(t, filepath.Join(files, "core.zip"), core)
			makeZip(t, filepath.Join(files, "ext.zip"), map[string]string{"x/readme": "x"})
			makeTgz(t, filepath.Join(files, "dotnet.tgz"), map[string]string{"dotnet": "host"})
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/Kxnrl/modsharp-public/releases/latest":
					w.Write([]byte(`{"tag_name":"git-300","assets":[{"name":"ModSharp-git300-linux.zip","browser_download_url":"` + addons.APIBase + `/dl/core.zip"},{"name":"ModSharp-git300-linux-extensions.zip","browser_download_url":"` + addons.APIBase + `/dl/ext.zip"}]}`))
				case "/dl/core.zip", "/dl/ext.zip":
					http.ServeFile(w, r, filepath.Join(files, filepath.Base(r.URL.Path)))
				case "/dotnet/" + tc.want + "/dotnet-runtime-" + tc.want + ".tgz":
					http.ServeFile(w, r, filepath.Join(files, "dotnet.tgz"))
				default:
					w.WriteHeader(404)
				}
			}))
			defer api.Close()
			addons.APIBase, addons.Mirrors, addons.DotNetURL = api.URL, nil, api.URL+"/dotnet/%s/dotnet-runtime-%s.tgz"
			env := &addons.Env{
				Root: root, Temp: filepath.Join(root, "temps"),
				Versions: addons.Versions{Path: filepath.Join(root, "versions.txt")},
				Log:      logx.New("T", logx.Debug, &bytes.Buffer{}), HTTP: api.Client(),
			}
			env.Versions.Set("ModSharp", "git227")
			env.Versions.Set("DotNet", "9.0.0")
			if err := env.UpdateModSharp(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := env.Versions.Get("DotNet"); got != tc.want {
				t.Fatalf("runtime %q, want %q", got, tc.want)
			}
		})
	}
}

// ModSharp runs alone (MetaMod aside): with it on, CSS and SwiftlyS2 must not
// load, and switching it off brings them back
func TestModSharpLoadsAlone(t *testing.T) {
	oldAPI, oldMirrors, oldURL := addons.APIBase, addons.Mirrors, addons.DotNetURL
	t.Cleanup(func() { addons.APIBase, addons.Mirrors, addons.DotNetURL = oldAPI, oldMirrors, oldURL })
	api := httptest.NewServer(http.NotFoundHandler())
	defer api.Close()
	addons.APIBase, addons.Mirrors, addons.DotNetURL = api.URL, nil, api.URL+"/%s/%s"
	root := t.TempDir()
	csgo := filepath.Join(root, "game", "csgo")
	vdf := filepath.Join(csgo, "addons", "metamod", "counterstrikesharp.vdf")
	os.MkdirAll(filepath.Dir(vdf), 0o755)
	os.WriteFile(vdf, []byte("css"), 0o644)
	gi := addons.Gameinfo{Path: filepath.Join(csgo, "gameinfo.gi")}
	os.WriteFile(gi.Path, []byte(gameinfo), 0o644)
	gi.Add("csgo/addons/swiftlys2")
	env := &addons.Env{
		Root: root, Temp: filepath.Join(root, "temps"),
		Versions: addons.Versions{Path: filepath.Join(root, "versions.txt")},
		Log:      logx.New("T", logx.Debug, &bytes.Buffer{}), HTTP: api.Client(),
	}
	loaded := func() (sharp, swiftly, css bool) {
		data, _ := os.ReadFile(gi.Path)
		_, err := os.Stat(vdf)
		return strings.Contains(string(data), "Game    sharp"), strings.Contains(string(data), "Game    csgo/addons/swiftlys2"), err == nil
	}
	sel := addons.Selection{Metamod: true, CSS: true, Swiftly: true, ModSharp: true}
	env.UpdateAll(context.Background(), sel, false)
	if sharp, swiftly, css := loaded(); !sharp || swiftly || css {
		t.Fatalf("ModSharp on: sharp=%v swiftly=%v css=%v, want only sharp", sharp, swiftly, css)
	}
	sel.ModSharp = false
	env.UpdateAll(context.Background(), sel, false)
	if sharp, swiftly, css := loaded(); sharp || !swiftly || !css {
		t.Fatalf("ModSharp off: sharp=%v swiftly=%v css=%v, want swiftly and css", sharp, swiftly, css)
	}
}
