// SPDX-License-Identifier: GPL-3.0-or-later

// Package steamcmd installs SteamCMD into the server volume and runs the
// app update the way the egg always did.
package steamcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

// InstallerURL is overridden by tests.
var InstallerURL = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz"

// Install downloads and unpacks steamcmd under root when it is missing.
// Errors are already reported on the console with their KL-STM code.
func Install(ctx context.Context, root string, client *http.Client, log *logx.Console) error {
	dir := filepath.Join(root, "steamcmd")
	if _, err := os.Stat(filepath.Join(dir, "steamcmd.sh")); err == nil {
		log.Log(logx.Debug, "SteamCMD already installed")
		return nil
	}
	log.Log(logx.Info, "Installing SteamCMD...")
	os.MkdirAll(dir, 0o755)
	os.MkdirAll(filepath.Join(root, "steamapps"), 0o755)
	archive := filepath.Join(root, "steamcmd.tar.gz")
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = addons.Download(ctx, client, InstallerURL, archive, nil); err == nil {
			break
		}
		log.Logf(logx.Warning, "Download failed (attempt %d/3)", attempt)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if err != nil {
		log.Code(logx.Error, "KL-STM-05", "Failed to download SteamCMD after 3 attempts")
		return err
	}
	if err := addons.ExtractTarGz(archive, dir); err != nil {
		log.Code(logx.Error, "KL-STM-06", "Failed to extract SteamCMD")
		return err
	}
	os.Remove(archive)
	if _, err := os.Stat(filepath.Join(dir, "steamcmd.sh")); err != nil {
		log.Code(logx.Error, "KL-STM-07", "steamcmd directory does not exist")
		return err
	}
	// first run unpacks the client
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "steamcmd.sh"), "+quit")
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = log.Out, log.Out
	cmd.Run()
	CopySteamClient(root, log)
	log.Log(logx.Success, "SteamCMD installed successfully")
	return nil
}

// CopySteamClient refreshes .steam/sdk32 and sdk64 from the steamcmd tree.
func CopySteamClient(root string, log *logx.Console) {
	for _, pair := range [][2]string{{"linux32", "sdk32"}, {"linux64", "sdk64"}} {
		src := filepath.Join(root, "steamcmd", pair[0], "steamclient.so")
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(root, ".steam", pair[1], "steamclient.so")
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := copyFile(src, dst); err != nil {
			log.Logf(logx.Warning, "Failed to copy %s libraries", pair[0])
		}
	}
}

// Options for Update.
type Options struct {
	AppID    string
	Login    string // empty = anonymous
	Password string
	BetaID   string
	BetaPass string
	Validate bool
}

// Args builds the steamcmd argument list; the login password is masked in
// the returned display string.
func (o Options) Args(root string) (args []string, display string) {
	if o.Login != "" {
		args = append(args, "+login", o.Login, o.Password)
	} else {
		args = append(args, "+login", "anonymous")
	}
	args = append(args, "+force_install_dir", root, "+app_update", o.AppID)
	if o.BetaID != "" {
		args = append(args, "-beta", o.BetaID)
		if o.BetaPass != "" {
			args = append(args, "-betapassword", o.BetaPass)
		}
	}
	if o.Validate {
		args = append(args, "validate")
	}
	args = append(args, "+quit")
	shown := append([]string{}, args...)
	if o.Login != "" {
		shown[2] = "****"
	}
	return args, strings.Join(shown, " ")
}

// Update runs app_update and reports failures with their KL-STM code.
// The 5 s abort window before a validate run is the caller's job.
func Update(ctx context.Context, root string, o Options, log *logx.Console) error {
	args, display := o.Args(root)
	log.Logf(logx.Debug, "SteamCMD command: ./steamcmd/steamcmd.sh %s", display)
	cmd := exec.CommandContext(ctx, filepath.Join(root, "steamcmd", "steamcmd.sh"), args...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = log.Out, log.Out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 8:
		log.Code(logx.Error, "KL-STM-01", "SteamCMD connection error (exit code 8)")
	case errors.As(err, &exitErr):
		log.Code(logx.Error, "KL-STM-02", fmt.Sprintf("SteamCMD failed with exit code %d", exitErr.ExitCode()))
	default:
		log.Code(logx.Error, "KL-STM-02", "SteamCMD failed: "+err.Error())
	}
	CopySteamClient(root, log)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
