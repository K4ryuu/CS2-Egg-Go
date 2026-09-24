// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Mirrors are tried after the original URL for github downloads, for hosts
// in regions where github.com is throttled.
//
// Empty on purpose. What comes back from a mirror is extracted into the
// addons directory and loaded as native code by the next server start, and
// there is nothing to check it against: GitHub publishes no hashes for
// these releases, so a mirror operator, or anyone who can take one of those
// domains, hands every container that happened to fail a GitHub fetch a
// backdoored Metamod. The daemon's own binary is a different case, and is
// verified against a checksums file from GitHub itself.
//
// Set ADDON_MIRRORS to a comma-separated list of prefixes to turn them back
// on, knowingly.
var Mirrors = MirrorsFor(os.Getenv("ADDON_MIRRORS"))

// MirrorsFor parses the operator's list, keeping only https prefixes.
func MirrorsFor(list string) []string {
	var out []string
	for _, m := range strings.Split(list, ",") {
		if m = strings.TrimSpace(m); strings.HasPrefix(m, "https://") {
			out = append(out, m)
		}
	}
	return out
}

// DownloadTimeout bounds one download attempt.
var DownloadTimeout = 300 * time.Second

// Download fetches url (plus mirrors for github URLs) into dest. An empty
// body counts as a failure. onRetry is told about each failed attempt. A
// file:// url is a file the node delivered into the volume: it is moved.
func Download(ctx context.Context, client *http.Client, url, dest string, onRetry func(url string, err error)) error {
	if p, ok := strings.CutPrefix(url, "file://"); ok {
		return takeLocal(p, dest)
	}
	urls := []string{url}
	if strings.Contains(url, "github.com") || strings.Contains(url, "githubusercontent.com") {
		for _, m := range Mirrors {
			urls = append(urls, m+url)
		}
	}
	var last error
	for _, u := range urls {
		last = fetch(ctx, client, u, dest)
		if last == nil {
			return nil
		}
		if onRetry != nil {
			onRetry(u, last)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("all download sources failed: %w", last)
}

// takeLocal moves (or copies) a delivered file into dest.
func takeLocal(src, dest string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return errors.New("delivered file is empty")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if os.Rename(src, dest) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

func fetch(ctx context.Context, client *http.Client, url, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return err
	}
	if n == 0 {
		os.Remove(dest)
		return errors.New("downloaded file is empty")
	}
	return nil
}

// safeJoin refuses archive entries that would escape dir.
// entryPath turns an archive entry name into a path inside the target dir.
// The result is relative, because every write goes through an os.Root on
// that dir: a lexical check alone cannot see a link that points at another
// link, and it cannot see an absolute symlink target at all.
func entryPath(name string) (string, error) {
	slash := filepath.ToSlash(name)
	// refused rather than normalised: an entry that tried to climb out is
	// worth failing the extraction over, not silently relocating
	if path.IsAbs(slash) {
		return "", fmt.Errorf("archive entry is an absolute path: %q", name)
	}
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." {
			return "", fmt.Errorf("archive entry escapes target dir: %q", name)
		}
	}
	rel := strings.TrimPrefix(path.Clean("/"+slash), "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("archive entry has no name")
	}
	return rel, nil
}

// openTarget opens dir as a root. Everything an archive asks for is created
// through it, so an entry can never reach outside dir however it is spelled.
func openTarget(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// ExtractZip unpacks src into dir, overwriting files.
func ExtractZip(src, dir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	root, err := openTarget(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, f := range r.File {
		rel, err := entryPath(f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(root, rel, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ExtractTarGz unpacks src into dir, overwriting files.
func ExtractTarGz(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	root, err := openTarget(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(rel, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
				return err
			}
			if err := writeFile(root, rel, tr, os.FileMode(h.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// an absolute target used to pass the old lexical check,
			// because filepath.Join rewrote it under the target dir while
			// os.Symlink stored it verbatim; a later entry then wrote
			// through it. Links stay relative and stay inside.
			if path.IsAbs(filepath.ToSlash(h.Linkname)) {
				return fmt.Errorf("archive entry %q links to an absolute path", h.Name)
			}
			if _, err := entryPath(path.Join(path.Dir(rel), filepath.ToSlash(h.Linkname))); err != nil {
				return err
			}
			root.Remove(rel)
			if err := root.Symlink(h.Linkname, rel); err != nil {
				return err
			}
		}
	}
}

func writeFile(root *os.Root, rel string, r io.Reader, mode os.FileMode) error {
	mode = mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	// never write through a link the archive planted a moment ago
	root.Remove(rel)
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// CopyTree copies src's contents into dst (like `cp -rf src/. dst/`).
//
// The source is an archive someone else built, so it goes through a root on
// dst: a link the archive carried must not be able to steer a write out of
// the addons directory, however it was spelled.
func CopyTree(src, dst string) error {
	root, err := openTarget(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		if r == "." {
			return nil
		}
		rel := filepath.ToSlash(r)
		if d.IsDir() {
			return root.MkdirAll(rel, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if path.IsAbs(filepath.ToSlash(link)) {
				return nil // an absolute link out of the tree is dropped
			}
			root.Remove(rel)
			return root.Symlink(link, rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
			return err
		}
		return writeFile(root, rel, in, info.Mode())
	})
}
