// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// DotNetVersion is the runtime ModSharp ships against. Pinned on purpose.
const DotNetVersion = "10.0.0"

// DotNetURL is overridden by tests.
var DotNetURL = "https://dotnetcli.azureedge.net/dotnet/Runtime/%s/dotnet-runtime-%s-linux-x64.tar.gz"

// Env is everything an updater needs.
type Env struct {
	Root       string // /home/container
	Temp       string // scratch dir, removed by the caller
	Versions   Versions
	Log        *logx.Console
	HTTP       *http.Client
	Prerelease bool
	// Node answers addon queries from the node's cache (release info and
	// files); nil means GitHub directly.
	Node func(q *proto.AddonQuery) *proto.AddonRelease
}

// Selection says which addons the panel asked for.
type Selection struct {
	Metamod, CSS, Swiftly, ModSharp bool
}

func (e *Env) addonsDir() string { return filepath.Join(e.Root, "game", "csgo", "addons") }
func (e *Env) gameinfo() Gameinfo {
	return Gameinfo{Path: filepath.Join(e.Root, "game", "csgo", "gameinfo.gi")}
}

// release fetches and logs the way every updater does.
func (e *Env) release(ctx context.Context, label, repo string, prerelease bool) (*Release, error) {
	if e.Node != nil {
		switch r := e.Node(&proto.AddonQuery{Repo: repo, Prerelease: prerelease}); {
		case r == nil:
		case r.Err != "" && r.Pinned:
			// the node holds this addon at a version it could not resolve;
			// installing the newest instead is exactly what a pin forbids
			e.Log.Logf(logx.Error, "%s is pinned on the node but the version could not be found (%s) - keeping the installed version", label, r.Err)
			return nil, errors.New("pinned version unavailable")
		case r.Err != "":
			e.Log.Logf(logx.Debug, "Node cache has no release for %s (%s), asking GitHub", repo, r.Err)
		case r.Tag != "":
			if r.Pinned {
				e.Log.Logf(logx.Info, "%s is pinned to %s by the node", label, r.Tag)
			} else {
				e.Log.Logf(logx.Debug, "Release info for %s from the node cache (%s)", repo, r.Tag)
			}
			rel := &Release{Tag: r.Tag, Prerelease: r.Prerelease, Pinned: r.Pinned}
			for _, a := range r.Assets {
				rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL})
			}
			return rel, nil
		}
	}
	if prerelease {
		e.Log.Logf(logx.Debug, "Checking releases (prereleases enabled) for %s", repo)
	} else {
		e.Log.Logf(logx.Debug, "Checking latest stable release for %s", repo)
	}
	rel, err := FetchRelease(ctx, e.HTTP, repo, prerelease)
	if err != nil {
		switch {
		case errors.Is(err, ErrRateLimited):
			e.Log.Logf(logx.Warning, "GitHub API rate limited - skipping update check for %s this boot", repo)
		default:
			e.Log.Logf(logx.Warning, "%v - skipping update check", err)
		}
		e.Log.Logf(logx.Error, "Failed to get release info for %s (%s)", label, repo)
		return nil, err
	}
	return rel, nil
}

// decide logs the verdict and says whether to install. A pinned release is
// installed whenever it differs from what is here, downgrade included: the
// node was told to hold this exact version.
func (e *Env) decide(label, key, latest string, pinned bool) bool {
	if latest == "" {
		e.Log.Logf(logx.Error, "Failed to get version for %s", label)
		return false
	}
	current := e.Versions.Get(key)
	if pinned {
		switch {
		case current == latest:
			e.Log.Logf(logx.Success, "%s is at its pinned version (%s)", label, latest)
			return false
		case current == "":
			e.Log.Logf(logx.Info, "Installing %s at its pinned version %s", label, latest)
		case NeedsUpdate(current, latest) == Downgrade:
			e.Log.Logf(logx.Warning, "Rolling %s back to the pinned version %s (installed: %s)", label, latest, current)
		default:
			e.Log.Logf(logx.Info, "Moving %s to the pinned version %s (installed: %s)", label, latest, current)
		}
		return true
	}
	switch NeedsUpdate(current, latest) {
	case UpToDate:
		e.Log.Logf(logx.Success, "%s is up-to-date (%s)", label, current)
		return false
	case Downgrade:
		e.Log.Logf(logx.Info, "%s is at a newer version (%s) than latest (%s). Skipping downgrade.", label, current, latest)
		return false
	}
	if current == "" {
		current = "none"
	}
	e.Log.Logf(logx.Info, "Update available for %s: %s (current: %s)", label, latest, current)
	return true
}

func (e *Env) download(ctx context.Context, url, dest string) error {
	if e.Node != nil {
		if r := e.Node(&proto.AddonQuery{URL: url}); r != nil && r.Err == "" && len(r.Assets) == 1 && r.Assets[0].Path != "" {
			e.Log.Logf(logx.Debug, "Taking %s from the node cache", r.Assets[0].Name)
			url = "file://" + filepath.Join(e.Root, "egg", r.Assets[0].Path)
		} else if r != nil && r.Err != "" {
			e.Log.Logf(logx.Debug, "Node cache miss (%s), downloading", r.Err)
		}
	}
	e.Log.Logf(logx.Debug, "Downloading from: %s", url)
	return Download(ctx, e.HTTP, url, dest, func(_ string, err error) {
		e.Log.Logf(logx.Warning, "Download failed, trying next mirror... (%v)", err)
	})
}

func (e *Env) fetchAndExtract(ctx context.Context, url, file, dir string) error {
	if err := e.download(ctx, url, file); err != nil {
		e.Log.Log(logx.Error, "All download sources failed")
		return err
	}
	var err error
	if strings.HasSuffix(file, ".zip") {
		err = ExtractZip(file, dir)
	} else {
		err = ExtractTarGz(file, dir)
	}
	if err != nil {
		e.Log.Logf(logx.Error, "Failed to extract %s: %v", filepath.Base(file), err)
	}
	return err
}

// UpdateMetamod: CS2 builds are prereleases; the version lives in the asset
// name (mmsource-2.0.0-git1391-linux.tar.gz), not the tag.
func (e *Env) UpdateMetamod(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(e.addonsDir(), "metamod")); err != nil {
		e.Log.Log(logx.Info, "Installing Metamod...")
	}
	rel, err := e.release(ctx, "Metamod", RepoMetamod, true)
	if err != nil {
		return err
	}
	asset, ok := rel.Asset(regexp.MustCompile(`linux\.tar\.gz$`))
	if !ok {
		e.Log.Log(logx.Error, "No Linux asset found in Metamod release")
		return errors.New("no metamod linux asset")
	}
	latest := regexp.MustCompile(`git[0-9]+`).FindString(asset.Name)
	if !e.decide("Metamod", "Metamod", latest, rel.Pinned) {
		return nil
	}
	tmp := filepath.Join(e.Temp, "metamod")
	if err := e.fetchAndExtract(ctx, asset.URL, filepath.Join(e.Temp, "metamod.tar.gz"), tmp); err != nil {
		return err
	}
	if err := CopyTree(filepath.Join(tmp, "addons"), e.addonsDir()); err != nil {
		// version file NOT bumped, so the updater retries next boot
		e.Log.Log(logx.Error, "Metamod copy failed - keeping previous version")
		return err
	}
	e.Versions.Set("Metamod", latest)
	e.Log.Logf(logx.Success, "Metamod updated to %s", latest)
	return nil
}

// UpdateCSS installs CounterStrikeSharp (with runtime) over addons/.
func (e *Env) UpdateCSS(ctx context.Context) error {
	return e.simpleZip(ctx, "CSS", "CSS", RepoCSS, `-with-runtime-linux-.*\.zip$`, "css", func(tmp string) error {
		return CopyTree(filepath.Join(tmp, "addons"), e.addonsDir())
	}, "CounterStrikeSharp")
}

// UpdateSwiftly installs SwiftlyS2. An existing install only gets bin/ and
// gamedata/ refreshed so user configs and plugins survive.
func (e *Env) UpdateSwiftly(ctx context.Context) error {
	return e.simpleZip(ctx, "SwiftlyS2", "Swiftly", RepoSwiftly, `linux.*with-runtimes\.zip`, "swiftly", func(tmp string) error {
		src := findDir(tmp, "swiftlys2", filepath.Join("addons", "swiftlys2"))
		if src == "" {
			e.Log.Log(logx.Error, "SwiftlyS2 directory not found in archive")
			return errors.New("swiftlys2 dir missing in archive")
		}
		target := filepath.Join(e.addonsDir(), "swiftlys2")
		if _, err := os.Stat(target); err == nil {
			for _, sub := range []string{"bin", "gamedata"} {
				if err := CopyTree(filepath.Join(src, sub), filepath.Join(target, sub)); err != nil {
					return err
				}
			}
			return nil
		}
		return CopyTree(src, target)
	}, "SwiftlyS2")
}

func (e *Env) simpleZip(ctx context.Context, label, key, repo, pattern, tmpName string, install func(tmp string) error, human string) error {
	tmp := filepath.Join(e.Temp, tmpName)
	os.RemoveAll(tmp)
	os.MkdirAll(tmp, 0o755)
	rel, err := e.release(ctx, label, repo, e.Prerelease)
	if err != nil {
		return err
	}
	if !e.decide(label, key, rel.Tag, rel.Pinned) {
		return nil
	}
	asset, ok := rel.Asset(regexp.MustCompile(pattern))
	if !ok {
		e.Log.Logf(logx.Error, "No suitable asset found for %s", repo)
		return nil
	}
	if err := e.fetchAndExtract(ctx, asset.URL, filepath.Join(tmp, "download.zip"), tmp); err != nil {
		return err
	}
	if err := install(tmp); err != nil {
		e.Log.Logf(logx.Error, "%s copy failed - keeping previous version", human)
		return err
	}
	e.Versions.Set(key, rel.Tag)
	e.Log.Logf(logx.Success, "%s updated to %s", human, rel.Tag)
	return nil
}

// UpdateModSharp installs the pinned .NET runtime, then core into game/ and
// extensions into game/sharp/shared/, keeping core.json and admins.jsonc.
func (e *Env) UpdateModSharp(ctx context.Context) error {
	sharp := filepath.Join(e.Root, "game", "sharp")
	if err := e.dotnet(ctx, sharp); err != nil {
		e.Log.Log(logx.Error, "Failed to install .NET runtime, aborting ModSharp update")
		return err
	}
	rel, err := e.release(ctx, "ModSharp", RepoModSharp, e.Prerelease)
	if err != nil {
		return err
	}
	latest := strings.ReplaceAll(rel.Tag, "-", "")
	var core, ext Asset
	for _, a := range rel.Assets {
		switch {
		case strings.Contains(a.Name, "linux-extensions.zip"):
			ext = a
		case strings.Contains(a.Name, "linux.zip") && !strings.Contains(a.Name, "extensions"):
			core = a
		}
	}
	if latest == "" || core.URL == "" || ext.URL == "" {
		e.Log.Log(logx.Error, "Could not parse ModSharp release data")
		return errors.New("modsharp release incomplete")
	}
	if !e.decide("ModSharp", "ModSharp", latest, rel.Pinned) {
		return nil
	}
	keep := map[string][]byte{}
	for _, f := range []string{"core.json", "admins.jsonc"} {
		if data, err := os.ReadFile(filepath.Join(sharp, "configs", f)); err == nil {
			keep[f] = data
			e.Log.Logf(logx.Debug, "Backed up %s", f)
		}
	}
	if err := e.fetchAndExtract(ctx, core.URL, filepath.Join(e.Temp, filepath.Base(core.URL)), filepath.Join(e.Root, "game")); err != nil {
		e.Log.Log(logx.Error, "Failed to install ModSharp core")
		return err
	}
	if err := e.fetchAndExtract(ctx, ext.URL, filepath.Join(e.Temp, filepath.Base(ext.URL)), filepath.Join(sharp, "shared")); err != nil {
		e.Log.Log(logx.Warning, "Failed to install ModSharp extensions")
	}
	for f, data := range keep {
		os.MkdirAll(filepath.Join(sharp, "configs"), 0o755)
		if err := os.WriteFile(filepath.Join(sharp, "configs", f), data, 0o644); err == nil {
			e.Log.Logf(logx.Success, "Restored %s config", f)
		}
	}
	e.Versions.Set("ModSharp", latest)
	e.Log.Logf(logx.Success, "ModSharp updated to %s", latest)
	return nil
}

func (e *Env) dotnet(ctx context.Context, sharp string) error {
	runtime := filepath.Join(sharp, "runtime")
	if e.Versions.Get("DotNet") == DotNetVersion {
		if _, err := os.Stat(filepath.Join(runtime, "dotnet")); err == nil {
			e.Log.Logf(logx.Debug, ".NET runtime already up to date: %s", DotNetVersion)
			return nil
		}
	}
	e.Log.Logf(logx.Running, "Installing .NET %s runtime...", DotNetVersion)
	os.MkdirAll(runtime, 0o755)
	url := fmt.Sprintf(DotNetURL, DotNetVersion, DotNetVersion)
	if err := e.fetchAndExtract(ctx, url, filepath.Join(e.Temp, "dotnet-runtime.tar.gz"), runtime); err != nil {
		e.Log.Log(logx.Error, "Failed to install .NET runtime")
		return err
	}
	e.Versions.Set("DotNet", DotNetVersion)
	e.Log.Logf(logx.Success, ".NET %s runtime installed successfully", DotNetVersion)
	return nil
}

// UpdateAll runs the selected updaters and the gameinfo.gi patches in the
// order the egg always used. Errors are logged per addon, never fatal.
func (e *Env) UpdateAll(ctx context.Context, sel Selection, allowTokenless bool) {
	os.MkdirAll(e.Temp, 0o755)
	defer os.RemoveAll(e.Temp)
	gi := e.gameinfo()
	if sel.CSS && !sel.Metamod {
		// a Metamod that is already installed is somebody's deliberate
		// choice, usually to hold a version. Say so instead of crying wolf
		if _, err := os.Stat(filepath.Join(e.addonsDir(), "metamod")); err == nil {
			e.Log.Log(logx.Info, "MetaMod:Source is installed but not managed by the egg (INSTALL_METAMOD=0), leaving it as it is")
		} else {
			e.Log.Log(logx.Warning, "CounterStrikeSharp requires MetaMod:Source, but MetaMod is not enabled (INSTALL_METAMOD=0).")
			e.Log.Logf(logx.Error, "MetaMod directory not found at %s. CounterStrikeSharp will fail to load until MetaMod is installed.", filepath.Join(e.addonsDir(), "metamod"))
		}
	}
	if sel.ModSharp || gi.HasSharp() {
		if sel.CSS {
			e.Log.Log(logx.Warning, "ModSharp is present alongside CounterStrikeSharp. These addons may be incompatible and may cause conflicts. It is recommended to use only one of them.")
		}
		if sel.Swiftly {
			e.Log.Log(logx.Warning, "ModSharp is present alongside SwiftlyS2. These addons may be incompatible and may cause conflicts. It is recommended to use only one of them.")
		}
	}
	if sel.Metamod {
		e.UpdateMetamod(ctx)
		e.addPath(gi, "csgo/addons/metamod")
	} else if sel.CSS {
		if _, err := os.Stat(filepath.Join(e.addonsDir(), "metamod")); err == nil {
			e.addPath(gi, "csgo/addons/metamod")
		}
	}
	if sel.CSS {
		e.UpdateCSS(ctx)
	}
	if sel.Swiftly {
		e.UpdateSwiftly(ctx)
		e.addPath(gi, "csgo/addons/swiftlys2")
		old := filepath.Join(e.addonsDir(), "metamod", "swiftlys2.vdf")
		if err := os.Remove(old); err == nil {
			e.Log.Log(logx.Debug, "Removed old swiftlys2.vdf from metamod")
		}
	}
	if sel.ModSharp {
		e.UpdateModSharp(ctx)
		e.addPath(gi, "sharp")
	}
	switch changed, err := gi.MetamodFirst(); {
	case err != nil && !errors.Is(err, errNoGameinfo):
		e.Log.Logf(logx.Error, "Failed to reposition MetaMod: %v", err)
	case changed:
		e.Log.Log(logx.Success, "MetaMod repositioned successfully")
	}
	if _, err := gi.SetTokenless(allowTokenless); err != nil && !errors.Is(err, errNoGameinfo) {
		e.Log.Logf(logx.Error, "Failed to patch RequireLoginForDedicatedServers: %v", err)
	}
}

func (e *Env) addPath(gi Gameinfo, addon string) {
	changed, err := gi.Add(addon)
	switch {
	case errors.Is(err, errNoGameinfo):
		e.Log.Logf(logx.Error, "gameinfo.gi not found at %s", gi.Path)
	case err != nil:
		e.Log.Logf(logx.Error, "Failed to add %s to gameinfo.gi: %v", addon, err)
	case changed:
		e.Log.Logf(logx.Info, "Added %s to gameinfo.gi", addon)
	default:
		e.Log.Logf(logx.Debug, "%s already in gameinfo.gi", addon)
	}
}

// findDir returns the first directory named name whose path ends in suffix.
func findDir(root, name, suffix string) string {
	var found string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() && d.Name() == name && strings.HasSuffix(path, suffix) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
