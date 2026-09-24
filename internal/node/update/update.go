// SPDX-License-Identifier: GPL-3.0-or-later

// Package update keeps cs2node current from GitHub Releases: the channel
// picks the release, the checksum file vouches for the binary, the swap is
// atomic and keeps the previous binary for a rollback.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// Repo the releases come from.
const Repo = "K4ryuu/CS2-Egg-Go"

// AssetName is the node binary's asset name.
const AssetName = "cs2node_linux_amd64"

// Candidate is a newer release than the running one.
type Candidate struct {
	Version   string
	Binary    addons.Asset
	Checksums addons.Asset
}

// ErrUpToDate: nothing newer on the channel.
var ErrUpToDate = errors.New("already up to date")

// Check finds the newest release on the channel. "dev" never updates,
// "stable" takes stable releases only, "beta" takes prereleases too.
func Check(ctx context.Context, client *http.Client, channel, current string) (*Candidate, error) {
	switch channel {
	case "dev":
		return nil, ErrUpToDate
	case "stable", "beta":
	default:
		return nil, fmt.Errorf("unknown channel %q", channel)
	}
	rel, err := addons.FetchRelease(ctx, client, Repo, channel == "beta")
	if err != nil {
		return nil, err
	}
	if version.Compare(rel.Tag, current) <= 0 {
		return nil, ErrUpToDate
	}
	bin, ok := rel.Asset(regexp.MustCompile("^" + regexp.QuoteMeta(AssetName) + "$"))
	if !ok {
		return nil, fmt.Errorf("release %s has no %s asset", rel.Tag, AssetName)
	}
	sums, ok := rel.Asset(regexp.MustCompile(`^checksums\.txt$`))
	if !ok {
		return nil, fmt.Errorf("release %s has no checksums.txt", rel.Tag)
	}
	// refuse here rather than at download time, so a release pointing
	// somewhere else is a clear "no" and not a failed fetch
	for _, a := range []addons.Asset{bin, sums} {
		if !FromGitHub(a.URL) {
			return nil, fmt.Errorf("%w: %s", ErrNotGitHub, a.URL)
		}
	}
	return &Candidate{Version: strings.TrimPrefix(rel.Tag, "v"), Binary: bin, Checksums: sums}, nil
}

// Apply downloads the candidate next to binPath, verifies its sha256
// against checksums.txt, then swaps it in keeping the old one as .prev.
// Nothing inside the release can skip the check: the checksum is the only
// policy, there are no flags to honour, and both files come straight from
// GitHub with no mirror in between.
func Apply(ctx context.Context, client *http.Client, c *Candidate, binPath string) error {
	dir := filepath.Dir(binPath)
	tmp := filepath.Join(dir, ".cs2node.new")
	sums := filepath.Join(dir, ".cs2node.sums")
	defer os.Remove(sums)
	if err := download(ctx, client, c.Checksums.URL, sums); err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	want, err := expectedSum(sums, AssetName)
	if err != nil {
		return err
	}
	if err := download(ctx, client, c.Binary.URL, tmp); err != nil {
		return fmt.Errorf("binary: %w", err)
	}
	got, err := fileSum(tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got != want {
		os.Remove(tmp)
		return fmt.Errorf("checksum mismatch for %s: got %s want %s", AssetName, got, want)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(binPath, binPath+".prev"); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, binPath)
}

// expectedSum reads "<sha256>  <name>" lines.
func expectedSum(path, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Restart asks systemd to restart the daemon (which is us, when called
// from the daemon: the new binary comes up).
func Restart() error {
	out, err := exec.Command("systemctl", "restart", "cs2node").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Loop checks once shortly after start and then every interval, applying
// what it finds. log receives one line per outcome, updated the new version
// right before the restart.
func Loop(ctx context.Context, client *http.Client, channel, binPath string, interval time.Duration, log func(string), updated func(to string)) {
	first := time.After(2 * time.Minute)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
		case <-t.C:
		}
		c, err := Check(ctx, client, channel, version.Version)
		switch {
		case errors.Is(err, ErrUpToDate):
			continue
		case err != nil:
			log("update check failed: " + err.Error())
			continue
		}
		if err := Apply(ctx, client, c, binPath); err != nil {
			log("update to " + c.Version + " failed: " + err.Error())
			continue
		}
		log("updated to " + c.Version + ", restarting")
		if updated != nil {
			updated(c.Version)
		}
		if err := Restart(); err != nil {
			log(err.Error())
		}
	}
}
