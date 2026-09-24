// SPDX-License-Identifier: GPL-3.0-or-later

package addoncache_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/addoncache"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

func TestReleaseIsFetchedOncePerRefreshAndStaleServesOnRateLimit(t *testing.T) {
	var calls int32
	var limited atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if limited.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"tag_name":"v1.2.3","prerelease":false,"assets":[{"name":"x-linux.zip","browser_download_url":"` + "http://example/x-linux.zip" + `"}]}`))
	}))
	defer srv.Close()
	old := addons.APIBase
	addons.APIBase = srv.URL
	defer func() { addons.APIBase = old }()

	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	c := addoncache.Open(t.TempDir(), srv.Client(), time.Hour)
	c.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		rel, err := c.Release(context.Background(), "a/b", false)
		if err != nil || rel.Tag != "v1.2.3" || len(rel.Assets) != 1 {
			t.Fatalf("release %d: %v %+v", i, err, rel)
		}
	}
	if calls != 1 {
		t.Fatalf("github called %d times within the refresh window", calls)
	}
	now = now.Add(2 * time.Hour)
	limited.Store(true)
	rel, err := c.Release(context.Background(), "a/b", false)
	if err != nil || rel.Tag != "v1.2.3" {
		t.Fatalf("stale copy must answer when github rate limits: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a refresh attempt, calls=%d", calls)
	}
	if _, err := c.Release(context.Background(), "never/seen", false); err == nil {
		t.Fatal("no cache and rate limited must fail")
	}
}

func TestFileDownloadsOnceEvenInParallelAndDelivers(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("zip-bytes"))
	}))
	defer srv.Close()
	c := addoncache.Open(t.TempDir(), srv.Client(), time.Hour)
	url := srv.URL + "/dl/addon-linux.zip"
	var wg sync.WaitGroup
	paths := make([]string, 4)
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := c.File(context.Background(), url)
			if err != nil {
				t.Error(err)
			}
			paths[i] = p
		}(i)
	}
	wg.Wait()
	if hits != 1 {
		t.Fatalf("downloaded %d times", hits)
	}
	for _, p := range paths[1:] {
		if p != paths[0] {
			t.Fatal("paths differ")
		}
	}
	if filepath.Base(paths[0]) != "addon-linux.zip" {
		t.Fatalf("basename kept: %s", paths[0])
	}
	vol := t.TempDir()
	rel, err := addoncache.Deliver(paths[0], vol, core.Owner{UID: os.Getuid(), GID: os.Getgid()})
	if err != nil || rel != "cache/addon-linux.zip" {
		t.Fatalf("deliver: %v %q", err, rel)
	}
	if data, _ := os.ReadFile(filepath.Join(vol, "egg", rel)); string(data) != "zip-bytes" {
		t.Fatalf("delivered content: %q", data)
	}
	files, bytes, _ := c.Stats()
	if files != 1 || bytes != 9 {
		t.Fatalf("stats: %d %d", files, bytes)
	}
}

func TestPruneDropsUnusedFiles(t *testing.T) {
	dir := t.TempDir()
	c := addoncache.Open(dir, nil, time.Hour)
	old := filepath.Join(dir, "files", "aaaa")
	fresh := filepath.Join(dir, "files", "bbbb")
	os.MkdirAll(old, 0o755)
	os.MkdirAll(fresh, 0o755)
	os.WriteFile(filepath.Join(old, "x"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(fresh, "y"), []byte("y"), 0o644)
	past := time.Now().Add(-40 * 24 * time.Hour)
	os.Chtimes(old, past, past)
	if n := c.Prune(30); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old dir kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh dir removed")
	}
	if c.Prune(0) != 0 {
		t.Fatal("keep_days 0 must prune nothing")
	}
}
