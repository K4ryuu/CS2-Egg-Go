// SPDX-License-Identifier: GPL-3.0-or-later

package workshop_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/nodeclient"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/workshop"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

type fakeDocker struct{ running []core.Container }

func (f *fakeDocker) Running(context.Context, []string) ([]core.Container, error) {
	return f.running, nil
}
func (f *fakeDocker) Inspect(_ context.Context, id string) (core.Container, error) {
	for _, c := range f.running {
		if c.ID == id {
			return c, nil
		}
	}
	return core.Container{}, errors.New("no such container")
}
func (f *fakeDocker) Events(context.Context, []string) (<-chan core.Event, <-chan error) {
	return make(chan core.Event), make(chan error)
}

// socket paths have a length limit, t.TempDir is too deep on macOS
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cs2w")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// waits Run out at cleanup, so its loops never read the next test's globals
func startWorkshop(t *testing.T, ctx context.Context, log *slog.Logger, client *http.Client, c *core.Core) {
	done := make(chan struct{})
	go func() { workshop.New(log, client).Run(ctx, c); close(done) }()
	t.Cleanup(func() { <-done })
}

func TestServersBootingTogetherAskSteamOnce(t *testing.T) {
	var asked atomic.Int32
	first, hold := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asked.Add(1) == 1 {
			close(first)
			<-hold
		}
		w.Write([]byte(`{"response":{"publishedfiledetails":[{"publishedfileid":"3070290869","result":1,"hcontent_file":"4513804927223833942","time_updated":1699419831}]}}`))
	}))
	defer srv.Close()
	old := workshop.DetailsURL
	workshop.DetailsURL = srv.URL
	t.Cleanup(func() { workshop.DetailsURL = old })

	dir := t.TempDir()
	store := workshop.Store{Dir: dir, Mount: "/tmp/cs2-workshop"}
	if _, err := store.Sync(volume(t, realACF), me(), true, nil); err != nil {
		t.Fatal(err)
	}
	cfg := nodeconfig.Default()
	wcfg := workshop.Defaults()
	wcfg.Dir = dir
	cfg.SetSection("workshop", wcfg)
	var conts []core.Container
	for i := range 4 {
		conts = append(conts, core.Container{ID: strconv.Itoa(i), Name: "srv" + strconv.Itoa(i), Volume: shortTempDir(t), Owner: me()})
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := core.New(&fakeDocker{running: conts}, cfg, []string{"workshop"}, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	startWorkshop(t, ctx, log, srv.Client(), c)
	select {
	case <-first: // the boot check holds Steam while the servers ask
	case <-time.After(5 * time.Second):
		t.Fatal("no boot check")
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(conts))
	for _, cont := range conts {
		wg.Go(func() {
			cl, err := nodeclient.Connect(ctx, core.SocketPath(cont.Volume), 3*time.Second, proto.Hello{BootID: "b"})
			if err != nil {
				errs <- err
				return
			}
			defer cl.Close()
			msg, err := cl.Request(&proto.WorkshopSync{}, 10*time.Second, func(m proto.Message) bool {
				_, ok := m.(*proto.WorkshopReady)
				return ok
			})
			if err == nil && msg.(*proto.WorkshopReady).Err != "" {
				err = errors.New(msg.(*proto.WorkshopReady).Err)
			}
			errs <- err
		})
	}
	time.Sleep(300 * time.Millisecond)
	close(hold)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("Steam asked %d times", n)
	}
}

// the boot check only knew the stored items, a new one was absorbed unchecked
func TestBootSyncAsksSteamAboutNewItems(t *testing.T) {
	booted := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(booted) })
		r.ParseForm()
		if !strings.Contains(r.PostForm.Encode(), "3070290869") {
			w.Write([]byte(`{"response":{"publishedfiledetails":[]}}`))
			return
		}
		w.Write([]byte(`{"response":{"publishedfiledetails":[{"publishedfileid":"3070290869","result":1,"hcontent_file":"8888888888888888888","time_updated":1799999999}]}}`))
	}))
	defer srv.Close()
	old := workshop.DetailsURL
	workshop.DetailsURL = srv.URL
	t.Cleanup(func() { workshop.DetailsURL = old })

	vol := shortTempDir(t)
	if err := os.CopyFS(vol, os.DirFS(volume(t, realACF))); err != nil {
		t.Fatal(err)
	}
	cfg := nodeconfig.Default()
	wcfg := workshop.Defaults()
	wcfg.Dir = t.TempDir()
	os.MkdirAll(filepath.Join(wcfg.Dir, workshop.AppID, "1"), 0o755) // so the boot check asks Steam at all
	cfg.SetSection("workshop", wcfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := core.New(&fakeDocker{running: []core.Container{{ID: "0", Name: "srv", Volume: vol, Owner: me()}}}, cfg, []string{"workshop"}, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	startWorkshop(t, ctx, log, srv.Client(), c)
	select {
	case <-booted:
	case <-time.After(5 * time.Second):
		t.Fatal("no boot check")
	}
	cl, err := nodeclient.Connect(ctx, core.SocketPath(vol), 3*time.Second, proto.Hello{BootID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	msg, err := cl.Request(&proto.WorkshopSync{}, 10*time.Second, func(m proto.Message) bool {
		_, ok := m.(*proto.WorkshopReady)
		return ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := msg.(*proto.WorkshopReady); r.Err != "" || r.Absorbed != 0 || r.Released != 1 {
		t.Fatalf("want the outdated item released, got %+v", r)
	}
	assertNotInstalled(t, vol)
}

// a stopped server has no container, the daemon must still see its links
func TestDaemonPruneKeepsWhatAStoppedServerLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":{"publishedfiledetails":[]}}`))
	}))
	defer srv.Close()
	vols := t.TempDir()
	stopped := filepath.Join(vols, "stopped")
	if err := os.CopyFS(stopped, os.DirFS(volume(t, realACF))); err != nil {
		t.Fatal(err)
	}
	deleted := volume(t, realACF)
	writeManifest(t, deleted, v2, v2)
	dir := t.TempDir()
	store := workshop.Store{Dir: dir, Mount: "/tmp/cs2-workshop"}
	for _, v := range []string{stopped, deleted} {
		if _, err := store.Sync(v, me(), false, nil); err != nil {
			t.Fatal(err)
		}
	}
	os.RemoveAll(deleted) // its server is gone, nothing links v2 now
	old := time.Now().Add(-60 * 24 * time.Hour)
	for _, m := range []string{v1, v2} {
		if err := os.Chtimes(filepath.Join(dir, workshop.AppID, "3070290869", m), old, old); err != nil {
			t.Fatal(err)
		}
	}
	oldURL, oldDirs, oldDelay := workshop.DetailsURL, core.VolumeDirs, workshop.PruneDelay
	workshop.DetailsURL, core.VolumeDirs, workshop.PruneDelay = srv.URL, []string{vols}, 0
	t.Cleanup(func() { workshop.DetailsURL, core.VolumeDirs, workshop.PruneDelay = oldURL, oldDirs, oldDelay })

	cfg := nodeconfig.Default()
	wcfg := workshop.Defaults()
	wcfg.Dir, wcfg.Seed = dir, false // seed off: a link is use, a stopped one too
	cfg.SetSection("workshop", wcfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := core.New(&fakeDocker{}, cfg, []string{"workshop"}, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	startWorkshop(t, ctx, log, srv.Client(), c)
	gone := filepath.Join(dir, workshop.AppID, "3070290869", v2)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(gone); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the unused version was never pruned")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, workshop.AppID, "3070290869", v1)); err != nil {
		t.Fatalf("the version a stopped server links went too: %v", err)
	}
}
