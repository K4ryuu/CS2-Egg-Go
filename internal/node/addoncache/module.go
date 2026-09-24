// SPDX-License-Identifier: GPL-3.0-or-later

package addoncache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Config is the "addoncache" section of the node config.
type Config struct {
	Enabled        bool   `json:"enabled"`
	Dir            string `json:"dir"`
	RefreshMinutes int    `json:"refresh_minutes"` // how long a release answer stays fresh
	KeepDays       int    `json:"keep_days"`       // drop files unused this long, 0 = keep

	// PinVersions holds every server at the versions below instead of the
	// newest release; the pins stay in the file when it is off.
	PinVersions bool   `json:"pin_versions"`
	PinMetamod  string `json:"pin_metamod,omitempty"`
	PinCSS      string `json:"pin_css,omitempty"`
	PinSwiftly  string `json:"pin_swiftly,omitempty"`
	PinModSharp string `json:"pin_modsharp,omitempty"`
	// PinPerServer overrides the pins above for one container, by its name.
	// An entry set to "" means that server follows the newest release even
	// while the others are held back.
	PinPerServer map[string]map[string]string `json:"pin_per_server,omitempty"`
}

// Defaults is what the wizard offers.
func Defaults() Config {
	return Config{Enabled: true, Dir: "/var/lib/cs2node/addoncache", RefreshMinutes: 60, KeepDays: 30}
}

// Check rejects what the daemon cannot run with.
func (c Config) Check() error {
	switch {
	case c.Dir == "" || c.Dir[0] != '/':
		return errors.New("dir must be an absolute path")
	case c.RefreshMinutes < 1:
		return errors.New("refresh_minutes must be at least 1")
	case c.KeepDays < 0:
		return errors.New("keep_days cannot be negative")
	}
	for server, pins := range c.PinPerServer {
		for key := range pins {
			if _, ok := addons.Pinnable[strings.TrimPrefix(key, "pin_")]; !ok {
				return fmt.Errorf("pin_per_server[%s] has an unknown addon %q", server, key)
			}
		}
	}
	return nil
}

// PinKey is the config key holding one addon's pinned version.
func PinKey(addon string) string { return "pin_" + addon }

// Pin is the version this container must run of the addon behind repo, or
// "" to follow the newest release.
func (c Config) Pin(container, repo string) string {
	if !c.PinVersions {
		return ""
	}
	addon := addons.KeyForRepo(repo)
	if addon == "" {
		return ""
	}
	if per, ok := c.PinPerServer[container]; ok {
		if v, set := per[PinKey(addon)]; set {
			return strings.TrimSpace(v) // an empty entry frees this one server
		}
	}
	switch addon {
	case "metamod":
		return strings.TrimSpace(c.PinMetamod)
	case "css":
		return strings.TrimSpace(c.PinCSS)
	case "swiftly":
		return strings.TrimSpace(c.PinSwiftly)
	case "modsharp":
		return strings.TrimSpace(c.PinModSharp)
	}
	return ""
}

// Module is the addoncache feature.
type Module struct {
	Log  *slog.Logger
	HTTP *http.Client

	cfg   Config
	cache *Cache
	busy  chan struct{} // bounds parallel answers: a container could ask in a loop

	mu       sync.Mutex
	inflight map[string]int // queries outstanding per container
}

// New returns the module with logging and HTTP wired.
func New(log *slog.Logger, client *http.Client) *Module {
	return &Module{Log: log, HTTP: client, busy: make(chan struct{}, 8)}
}

// A query comes from inside a container, so only what the egg would ask
// for is honoured: GitHub repos and release assets, the .NET runtime.
var (
	repoName     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	allowedHosts = []string{"github.com", "api.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "dotnetcli.azureedge.net", "builds.dotnet.microsoft.com", "dotnetcli.blob.core.windows.net"}
)

// Allowed reports whether the cache will fetch the URL.
func Allowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range allowedHosts {
		if host == h {
			return true
		}
	}
	return false
}

func (m *Module) Name() string { return "addoncache" }

func (m *Module) Describe() string {
	return "Addon cache: one GitHub fetch per node for Metamod, CSS, Swiftly, ModSharp and .NET, shared with every server"
}

func (m *Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "dir", Prompt: "Cache directory", Default: d.Dir},
		{Key: "refresh_minutes", Prompt: "Minutes a release answer stays fresh", Default: strconv.Itoa(d.RefreshMinutes), Kind: module.Int, Help: "eggs booting inside this window never touch GitHub"},
		{Key: "keep_days", Prompt: "Days to keep a file nobody asked for", Default: strconv.Itoa(d.KeepDays), Kind: module.Int, Help: "0 = keep forever"},
		{Key: "pin_versions", Prompt: "Lock addon versions", Default: "false", Kind: module.Bool,
			Help: "hold every server at versions you choose instead of the newest release, so one bad upstream build cannot reach the fleet"},
		{Key: "pin_metamod", Prompt: "Metamod version", Help: "e.g. 2.0.0-git1450 or git1450, empty = always the newest", When: pinned},
		{Key: "pin_css", Prompt: "CounterStrikeSharp version", Help: "e.g. v1.0.360, empty = always the newest", When: pinned},
		{Key: "pin_swiftly", Prompt: "SwiftlyS2 version", Help: "release tag, empty = always the newest", When: pinned},
		{Key: "pin_modsharp", Prompt: "ModSharp version", Help: "release tag, empty = always the newest", When: pinned},
	}
}

func pinned(a map[string]any) bool { return a["pin_versions"] == true }

// Notes is what the installer prints once the module is on.
func (m *Module) Notes() []string {
	return []string{
		"Servers ask the node for addon releases from their next boot; an egg that cannot reach the node still goes to GitHub itself.",
		"Version locks change any time without a restart: cs2node pin metamod <version>, cs2node pins, cs2node unpin <addon>. A pin applies on each server's next boot, downgrade included.",
	}
}

// Run serves until ctx ends.
func (m *Module) Run(ctx context.Context, c *core.Core) error {
	m.cfg = Defaults()
	if err := c.Config(m.Name(), &m.cfg); err != nil {
		return err
	}
	if err := m.cfg.Check(); err != nil {
		return err
	}
	if err := os.MkdirAll(m.cfg.Dir, 0o755); err != nil {
		return err
	}
	m.cache = Open(m.cfg.Dir, m.HTTP, time.Duration(m.cfg.RefreshMinutes)*time.Minute)
	files, bytes, rels := m.cache.Stats()
	m.Log.Info("addoncache up", "dir", m.cfg.Dir, "files", files, "mib", bytes>>20, "releases", rels)
	c.ProvideStatus(m.Name(), func() any {
		f, b, r := m.cache.Stats()
		return status{Files: f, Bytes: b, Releases: r}
	})
	// a download may take a while, so it must not stall the socket, but a
	// container that floods queries must not spawn a goroutine per line
	// either: answer parks on an 8 slot semaphore, so the producers would
	// pile up without this cap
	c.Handle(proto.TypeAddonQuery, func(container string, msg proto.Message) {
		q := msg.(*proto.AddonQuery)
		if !m.claim(container) {
			m.Log.Debug("addon queries already in flight for this server", "container", container)
			return
		}
		go func() {
			defer m.release(container)
			m.answer(ctx, c, container, q)
		}()
	})
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n := m.cache.Prune(m.cfg.KeepDays); n > 0 {
				m.Log.Info("unused addon files removed", "count", n)
			}
		}
	}
}

// status is the addoncache section of the control snapshot.
type status struct {
	Files    int   `json:"files"`
	Bytes    int64 `json:"bytes"`
	Releases int   `json:"releases"`
}

func (m *Module) answer(ctx context.Context, c *core.Core, container string, q *proto.AddonQuery) {
	select {
	case m.busy <- struct{}{}:
		defer func() { <-m.busy }()
	case <-ctx.Done():
		return
	}
	reply := &proto.AddonRelease{Repo: q.Repo, URL: q.URL, Prerelease: q.Prerelease}
	switch {
	case q.Repo != "" && !repoName.MatchString(q.Repo):
		reply.Err = "not a github repo"
	case q.URL != "" && !Allowed(q.URL):
		reply.Err = "host not cached"
		m.Log.Warn("addon query for a host outside the allow list", "container", container, "url", q.URL)
	case q.Repo != "":
		// the pins are read from disk, so `cs2node pin` lands on the next
		// server boot without restarting the daemon
		cfg := m.cfg
		if fresh := (Config{}); c.ConfigNow(m.Name(), &fresh) == nil && fresh.Dir != "" {
			cfg = fresh
		}
		var rel *addons.Release
		var err error
		if want := cfg.Pin(container, q.Repo); want != "" {
			reply.Pinned = true
			rel, err = m.cache.ReleaseByVersion(ctx, q.Repo, want)
			if err != nil {
				// never fall back to the newest: that is what the pin forbids
				reply.Err = err.Error()
				m.Log.Warn("pinned release not found, the server keeps what it has", "repo", q.Repo, "version", want, "err", err)
				break
			}
		} else if rel, err = m.cache.Release(ctx, q.Repo, q.Prerelease); err != nil {
			reply.Err = err.Error()
			m.Log.Warn("release fetch failed", "repo", q.Repo, "err", err)
			break
		}
		reply.Tag, reply.Prerelease = rel.Tag, rel.Prerelease
		for _, a := range rel.Assets {
			reply.Assets = append(reply.Assets, proto.AddonAsset{Name: a.Name, URL: a.URL})
		}
	case q.URL != "":
		var cont core.Container
		for _, x := range c.Containers() {
			if x.Name == container {
				cont = x
			}
		}
		if cont.Volume == "" {
			reply.Err = "volume unknown"
			break
		}
		path, err := m.cache.File(ctx, q.URL)
		if err != nil {
			reply.Err = err.Error()
			m.Log.Warn("addon download failed", "url", q.URL, "err", err)
			break
		}
		rel, err := Deliver(path, cont.Volume, cont.Owner)
		if err != nil {
			reply.Err = err.Error()
			m.Log.Warn("addon delivery failed", "container", container, "err", err)
			break
		}
		reply.Assets = []proto.AddonAsset{{Name: filepath.Base(path), URL: q.URL, Path: rel}}
		m.Log.Info("addon delivered", "container", container, "file", filepath.Base(path))
	default:
		reply.Err = "empty query"
	}
	if err := c.Send(container, reply); err != nil {
		m.Log.Warn("addon answer not delivered", "container", container, "err", err)
	}
}

// Doctor checks the cache dir and reports its size.
func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	const s = "Addon cache"
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf(s, "config section unreadable: "+err.Error())}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf(s, "config: "+err.Error())}
	}
	if st, err := os.Stat(cfg.Dir); err != nil {
		return []doctor.Check{doctor.Warnf(s, cfg.Dir+" missing (the daemon creates it on start)")}
	} else if !st.IsDir() {
		return []doctor.Check{doctor.Failf(s, cfg.Dir+" is not a directory")}
	}
	files, bytes, rels := Open(cfg.Dir, nil, 0).Stats()
	return []doctor.Check{doctor.Ok(s, fmt.Sprintf("%d file(s), %.0f MiB, %d release answer(s) under %s", files, float64(bytes)/(1<<20), rels, cfg.Dir))}
}

// MaxInFlight is how many queries one container may have outstanding. The
// egg asks once per addon at boot, so four is generous; past that it is a
// retry storm or a flood, and neither deserves a goroutine.
const MaxInFlight = 4

func (m *Module) claim(container string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight == nil {
		m.inflight = map[string]int{}
	}
	if m.inflight[container] >= MaxInFlight {
		return false
	}
	m.inflight[container]++
	return true
}

func (m *Module) release(container string) {
	m.mu.Lock()
	if m.inflight[container]--; m.inflight[container] <= 0 {
		delete(m.inflight, container)
	}
	m.mu.Unlock()
}
