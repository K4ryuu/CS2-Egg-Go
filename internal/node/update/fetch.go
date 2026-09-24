// SPDX-License-Identifier: GPL-3.0-or-later

package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ReleaseHosts are the only places a cs2node binary or its checksums may
// come from. The addon downloader falls back to community mirrors when
// GitHub is slow, which is right for a plugin zip and wrong here: a mirror
// that can serve the binary can serve a checksums.txt to match it, and then
// the hash proves nothing at all. So this path takes no mirror, and refuses
// anything that is not GitHub over TLS.
var ReleaseHosts = []string{"github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}

// MaxAsset bounds one download. The node binary is around 10 MB; this is
// the ceiling that stops a broken or hostile source filling the disk.
const MaxAsset = 128 << 20

// FromGitHub reports whether the URL is one this updater will fetch.
func FromGitHub(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range ReleaseHosts {
		if host == h {
			return true
		}
	}
	return false
}

// ErrNotGitHub is a release asset URL pointing somewhere this updater will
// not follow.
var ErrNotGitHub = errors.New("release asset is not on github over https")

// download writes one release asset to dest. Redirects are followed only
// while they stay on an allowed host, so a redirect cannot walk the update
// off GitHub.
func download(ctx context.Context, client *http.Client, raw, dest string) error {
	if !FromGitHub(raw) {
		return fmt.Errorf("%w: %s", ErrNotGitHub, raw)
	}
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !FromGitHub(req.URL.String()) {
			return fmt.Errorf("%w: %s", ErrNotGitHub, req.URL.Redacted())
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cs2node")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, MaxAsset+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		os.Remove(dest)
		return err
	case n == 0:
		os.Remove(dest)
		return errors.New("downloaded file is empty")
	case n > MaxAsset:
		os.Remove(dest)
		return fmt.Errorf("release asset is over %d MiB", MaxAsset>>20)
	}
	return nil
}
