// SPDX-License-Identifier: GPL-3.0-or-later

// Package addoncache fetches addon releases (Metamod, CSS, SwiftlyS2,
// ModSharp, the .NET runtime) once per node and hands them to every egg,
// so twenty servers booting after an update hit GitHub once, not twenty
// times, and never trip the API rate limit.
package addoncache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

// Cache is the on-disk store: releases/<owner>__<repo>__<kind>.json and
// files/<hash of url>/<basename>.
type Cache struct {
	Dir     string
	HTTP    *http.Client
	Refresh time.Duration // how long a release answer stays fresh
	Now     func() time.Time

	mu    sync.Mutex
	inUse map[string]*sync.Mutex // per url, so parallel boots download once
}

type cachedRelease struct {
	Fetched time.Time       `json:"fetched"`
	Release *addons.Release `json:"release"`
}

// Open returns the cache under dir.
func Open(dir string, client *http.Client, refresh time.Duration) *Cache {
	return &Cache{Dir: dir, HTTP: client, Refresh: refresh, Now: time.Now, inUse: map[string]*sync.Mutex{}}
}

func (c *Cache) releasePath(repo string, prerelease bool) string {
	kind := "latest"
	if prerelease {
		kind = "pre"
	}
	return filepath.Join(c.Dir, "releases", strings.ReplaceAll(repo, "/", "__")+"__"+kind+".json")
}

// Release returns the repo's release, from disk while fresh, else from
// GitHub; when GitHub fails (rate limit) a stale copy still answers.
func (c *Cache) Release(ctx context.Context, repo string, prerelease bool) (*addons.Release, error) {
	path := c.releasePath(repo, prerelease)
	var have cachedRelease
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &have)
	}
	now := c.Now()
	if have.Release != nil && now.Sub(have.Fetched) < c.Refresh {
		return have.Release, nil
	}
	rel, err := addons.FetchRelease(ctx, c.HTTP, repo, prerelease)
	if err != nil {
		if have.Release != nil {
			return have.Release, nil // stale beats nothing
		}
		return nil, err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if data, err := json.Marshal(cachedRelease{Fetched: now, Release: rel}); err == nil {
		os.WriteFile(path, data, 0o644)
	}
	return rel, nil
}

// ReleaseByVersion returns the release holding want: the exact tag when
// there is one, otherwise the newest release whose tag or asset names
// carry it (Metamod's build number lives only in the asset name). A pin
// never moves, so the answer is cached for good.
func (c *Cache) ReleaseByVersion(ctx context.Context, repo, want string) (*addons.Release, error) {
	path := filepath.Join(c.Dir, "releases", strings.ReplaceAll(repo, "/", "__")+"__pin__"+safe(want)+".json")
	if data, err := os.ReadFile(path); err == nil {
		var have cachedRelease
		if json.Unmarshal(data, &have) == nil && have.Release != nil {
			return have.Release, nil
		}
	}
	rel, err := addons.FetchReleaseTag(ctx, c.HTTP, repo, want)
	if err != nil {
		list, lerr := addons.FetchReleases(ctx, c.HTTP, repo)
		if lerr != nil {
			return nil, lerr
		}
		for i := range list {
			if list[i].Holds(want) {
				rel = &list[i]
				break
			}
		}
		if rel == nil {
			return nil, fmt.Errorf("no release of %s carries version %q", repo, want)
		}
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if data, err := json.Marshal(cachedRelease{Fetched: c.Now(), Release: rel}); err == nil {
		os.WriteFile(path, data, 0o644)
	}
	return rel, nil
}

// safe turns a version into a file name.
func safe(v string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, v)
}

// File returns the cached path of url, downloading it (with the egg's
// mirrors) when missing.
func (c *Cache) File(ctx context.Context, url string) (string, error) {
	c.mu.Lock()
	lock, ok := c.inUse[url]
	if !ok {
		lock = &sync.Mutex{}
		c.inUse[url] = lock
	}
	c.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	sum := sha256.Sum256([]byte(url))
	dir := filepath.Join(c.Dir, "files", hex.EncodeToString(sum[:8]))
	name := filepath.Base(strings.SplitN(url, "?", 2)[0])
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	path := filepath.Join(dir, name)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		now := c.Now()
		os.Chtimes(dir, now, now) // last use, for pruning
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := path + ".part"
	if err := addons.Download(ctx, c.HTTP, url, tmp, nil); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// Deliver copies a cached file into the volume's egg/cache dir, owned by
// the volume owner. Always a copy: a hardlink would share the inode with
// every other server, and one chown or edit would poison the cache for all
// of them. Writes go through os.Root, so a symlink planted in the volume
// cannot redirect root. Returns the path relative to the egg dir.
func Deliver(cached, volume string, owner core.Owner) (string, error) {
	root, err := os.OpenRoot(volume)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.MkdirAll("egg/cache", 0o755); err != nil {
		return "", err
	}
	root.Lchown("egg", owner.UID, owner.GID)
	root.Lchown("egg/cache", owner.UID, owner.GID)
	name := filepath.Base(cached)
	rel := "egg/cache/" + name
	in, err := os.Open(cached)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := rel + ".part"
	root.Remove(tmp)
	out, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		root.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	root.Chown(tmp, owner.UID, owner.GID)
	root.Remove(rel)
	if err := root.Rename(tmp, rel); err != nil {
		return "", err
	}
	return "cache/" + name, nil
}

// Prune drops files unused for keepDays (0 = keep everything) and reports
// what stayed.
func (c *Cache) Prune(keepDays int) (removed int) {
	if keepDays <= 0 {
		return 0
	}
	cut := c.Now().Add(-time.Duration(keepDays) * 24 * time.Hour)
	entries, _ := os.ReadDir(filepath.Join(c.Dir, "files"))
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && e.IsDir() && info.ModTime().Before(cut) {
			if os.RemoveAll(filepath.Join(c.Dir, "files", e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}

// Stats counts what the cache holds.
func (c *Cache) Stats() (files int, bytes int64, releases int) {
	filepath.WalkDir(filepath.Join(c.Dir, "files"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasSuffix(d.Name(), ".part") {
			if info, err := d.Info(); err == nil {
				files++
				bytes += info.Size()
			}
		}
		return nil
	})
	rels, _ := os.ReadDir(filepath.Join(c.Dir, "releases"))
	return files, bytes, len(rels)
}
