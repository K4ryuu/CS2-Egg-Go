// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
)

// InstallerURL is overridden by tests.
var InstallerURL = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz"

// ensureSteamcmd installs steamcmd into dir when steamcmd.sh is missing.
func ensureSteamcmd(ctx context.Context, dir string, client *http.Client, out io.Writer) error {
	if _, err := os.Stat(filepath.Join(dir, "steamcmd.sh")); err == nil {
		return nil
	}
	os.MkdirAll(dir, 0o755)
	archive := filepath.Join(dir, "steamcmd_linux.tar.gz")
	if err := addons.Download(ctx, client, InstallerURL, archive, nil); err != nil {
		return fmt.Errorf("download steamcmd: %w", err)
	}
	defer os.Remove(archive)
	if err := addons.ExtractTarGz(archive, dir); err != nil {
		return fmt.Errorf("extract steamcmd: %w", err)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "steamcmd.sh"), "+quit")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Run() // first run unpacks the client; its exit code is noise
	return nil
}

// appUpdate runs the CS2 update into cs2Dir and returns the new buildid.
// steamclient.so lands in cs2Dir/.steam/sdk32|64 and the tree is made
// world-readable (owner untouched: containers only read it).
func appUpdate(ctx context.Context, steamcmdDir, cs2Dir string, validate bool, out io.Writer) (string, error) {
	args := []string{"+force_install_dir", cs2Dir, "+login", "anonymous", "+app_update", "730"}
	if validate {
		args = append(args, "validate")
	}
	args = append(args, "+quit")
	cmd := exec.CommandContext(ctx, filepath.Join(steamcmdDir, "steamcmd.sh"), args...)
	cmd.Dir = steamcmdDir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return BuildID(cs2Dir), fmt.Errorf("steamcmd: %w", err)
	}
	for _, pair := range [][2]string{{"linux32", "sdk32"}, {"linux64", "sdk64"}} {
		src := filepath.Join(steamcmdDir, pair[0], "steamclient.so")
		dst := filepath.Join(cs2Dir, ".steam", pair[1], "steamclient.so")
		copyFile(src, dst)
	}
	worldReadable(cs2Dir)
	id := BuildID(cs2Dir)
	if id == "" {
		return "", errNoBuild
	}
	return id, nil
}

// worldReadable is chmod -R u=rwX,go=rX.
func worldReadable(root string) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mode := info.Mode().Perm()
		if d.IsDir() || mode&0o111 != 0 {
			os.Chmod(path, 0o755)
		} else {
			os.Chmod(path, 0o644)
		}
		return nil
	})
}
