// SPDX-License-Identifier: GPL-3.0-or-later

// Package boot is the egg's startup sequence: configs, node handshake,
// SteamCMD, addons, cleanup, console setup, then the server run.
package boot

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/cleanup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/configs"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/console"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/guard"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/nodeclient"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/startup"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/steamcmd"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/supervisor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/watch"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// defaultPrefix is used before logging.json is loaded (and if it is ever
// missing the key), since the config defines the real prefix from then on.
const defaultPrefix = "KitsuneLab"

// Env is the process environment boot runs in.
type Env struct {
	Root   string // /home/container
	Getenv func(string) string
	Stdin  *os.File
	Stdout *os.File
	HTTP   *http.Client
}

func (e Env) flag(name string) bool { return strings.TrimSpace(e.Getenv(name)) == "1" }

// OverrideString is the shape every value-carrying Variable in this egg
// uses: the panel wins when it is set, the config file's own value
// otherwise. Unlike MigratedPrefix, nothing is written back, there was
// never an old value in a Variable to carry forward for these.
func OverrideString(fromEnv, fallback string) string {
	if fromEnv != "" {
		return fromEnv
	}
	return fallback
}

// OverrideInt is OverrideString for a Variable that must parse as a
// number; an unset or unparseable value is treated the same, the config
// file's own value wins.
func OverrideInt(fromEnv string, fallback int) int {
	if n, err := strconv.Atoi(fromEnv); err == nil {
		return n
	}
	return fallback
}

// OverrideList is OverrideString for a Variable that carries a
// comma-separated list (this egg's ADDON_MIRRORS shape), for a config
// field short of the structure patterns or rules need: clean tokens with
// no comma of their own, like a SteamID or an IP. Entries are trimmed,
// empty ones dropped; an unset or entirely-empty Variable returns nil, so
// the config file's own list wins.
func OverrideList(fromEnv string, fallback []string) []string {
	if strings.TrimSpace(fromEnv) == "" {
		return fallback
	}
	var out []string
	for _, s := range strings.Split(fromEnv, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if out == nil {
		return fallback
	}
	return out
}

// MigratedPrefix is the value a pre-1.2.4 install's PREFIX_TEXT variable
// should fold into logging.json, or "" when there is nothing to migrate:
// the variable is unset, already at its own default, or the file already
// carries a value someone set (migrated before, or edited directly).
func MigratedPrefix(fromEnv, filePrefix string) string {
	if fromEnv == "" || fromEnv == defaultPrefix || filePrefix != defaultPrefix {
		return ""
	}
	return fromEnv
}

// Run performs the whole boot and server run. It returns the process exit
// code for main.
func Run(ctx context.Context, env Env) int {
	eggDir := filepath.Join(env.Root, "egg")
	log := logx.New(defaultPrefix, logx.Info, env.Stdout)
	log.Mask = console.Masker(env.Getenv("STEAM_ACC"))

	os.MkdirAll(filepath.Join(eggDir, "configs"), 0o755)
	cfg, events, err := configs.Load(filepath.Join(eggDir, "configs"))
	if err != nil {
		log.Logf(logx.Error, "Config load failed: %v", err)
		return 1
	}
	cfg.Logging.Logging.ConsoleLevel = OverrideString(env.Getenv("CONSOLE_LOG_LEVEL"), cfg.Logging.Logging.ConsoleLevel)
	log.Min = logx.ParseLevel(cfg.Logging.Logging.ConsoleLevel)
	if p := MigratedPrefix(env.Getenv("PREFIX_TEXT"), cfg.Logging.Logging.Prefix); p != "" {
		cfg.Logging.Logging.Prefix = p
		if err := configs.MigratePrefix(filepath.Join(eggDir, "configs"), p); err != nil {
			log.Logf(logx.Warning, "Could not migrate the old PREFIX_TEXT variable into logging.json: %v", err)
		} else {
			log.Logf(logx.Debug, "Migrated PREFIX_TEXT %q from the panel variable into logging.json, once", p)
		}
	}
	if cfg.Logging.Logging.Prefix != "" {
		log.Prefix = cfg.Logging.Logging.Prefix
	}
	if cfg.Logging.Logging.FileEnabled {
		logsDir := filepath.Join(eggDir, "logs")
		os.MkdirAll(logsDir, 0o755)
		log.File = &logx.FileSink{Dir: logsDir}
		logx.Compress(logsDir, time.Now())
		logx.Rotate(logsDir, cfg.Logging.Logging.MaxDays, cfg.Logging.Logging.MaxFiles, cfg.Logging.Logging.MaxSizeMB, time.Now())
	}
	for _, ev := range events {
		switch {
		case ev.Report.Warning != "":
			log.Log(logx.Warning, ev.Report.Warning)
		case ev.Report.Migrated:
			log.Logf(logx.Debug, "Migrated %s to config version %s", ev.File, configs.SchemaVersion)
		}
	}
	log.Logf(logx.Debug, "cs2egg %s (%s, built %s)", version.Version, version.Channel, version.Built)

	stopUpdate := env.flag("SRCDS_STOP_UPDATE")
	node, managed := handshake(ctx, env, eggDir, log)
	if managed {
		stopUpdate = true
		removeStale(env.Root, log)
	} else {
		pruneBrokenVPKLinks(filepath.Join(env.Root, "game"), log)
	}

	if !stopUpdate {
		steamcmd.Install(ctx, env.Root, env.HTTP, log)
		if appID := env.Getenv("SRCDS_APPID"); appID != "" {
			opts := steamcmd.Options{
				AppID: appID, Login: env.Getenv("SRCDS_LOGIN"), Password: env.Getenv("SRCDS_LOGIN_PASS"),
				BetaID: env.Getenv("SRCDS_BETAID"), BetaPass: env.Getenv("SRCDS_BETAPASS"), Validate: env.flag("SRCDS_VALIDATE"),
			}
			if opts.Validate {
				log.Log(logx.Warning, "!!! VALIDATION ENABLED: THIS MAY WIPE CUSTOM CONFIGURATIONS!")
				log.Log(logx.Warning, "  → Starting in 5 seconds, stop the server NOW to abort.")
				select {
				case <-ctx.Done():
					return 0
				case <-time.After(5 * time.Second):
				}
			}
			steamcmd.Update(ctx, env.Root, opts, log)
		}
	}

	switch {
	case node != nil && node.HasModule("cleanup"):
		// the node runs the same rules on a schedule, off the boot path
		log.Log(logx.Debug, "Cleanup is handled by cs2node, skipping the boot-time pass")
	case env.flag("CLEANUP_ENABLED"):
		runCleanup(env.Root, cfg.Cleanup, log)
	}
	addonEnv := &addons.Env{
		Root: env.Root, Temp: filepath.Join(env.Root, "temps"),
		Versions: addons.Versions{Path: filepath.Join(eggDir, "versions.txt")},
		Log:      log, HTTP: env.HTTP, Prerelease: env.flag("PRERELEASE"),
	}
	if node != nil && node.HasModule("addoncache") {
		log.Log(logx.Debug, "Addon releases and downloads go through the node cache")
		addonEnv.Node = func(q *proto.AddonQuery) *proto.AddonRelease {
			m, err := node.Request(q, AddonCacheTimeout, func(m proto.Message) bool {
				r, ok := m.(*proto.AddonRelease)
				return ok && r.Repo == q.Repo && r.URL == q.URL
			})
			if err != nil {
				log.Logf(logx.Debug, "Node cache did not answer (%v), asking GitHub", err)
				return nil
			}
			return m.(*proto.AddonRelease)
		}
	}
	if node != nil && node.HasModule("workshop") {
		syncWorkshop(node, log)
	}
	addonEnv.UpdateAll(ctx, selection(env), env.flag("ALLOW_TOKENLESS"))

	pipeline := &console.Pipeline{Log: log, Out: env.Stdout, Mask: log.Mask}
	if env.flag("ENABLE_FILTER") {
		pipeline.Filter = console.NewFilter(cfg.Filter.Patterns)
		pipeline.Preview = cfg.Filter.PreviewMode
		e, c := pipeline.Filter.Counts()
		log.Logf(logx.Success, "Console filter active: %d patterns (%d exact, %d contains)", e+c, e, c)
	}
	var g *guard.Guard
	var nodeSink *NodeLogSink
	link := &nodeLink{client: node, path: filepath.Join(eggDir, nodeclient.SocketName), hello: nodeHello(env)}
	if env.flag("ENABLE_LOG_FILES") {
		if link.hasModule("consolelog") {
			nodeSink = NewNodeLogSink(ctx, link.send)
			log.File = logx.FanOut{log.File, nodeSink}
			log.Log(logx.Success, "Console log forwarding to the node active")
		} else {
			log.Log(logx.Warning, "Log File Persistence is on, but the node's console-log module is not: nothing is being saved. Install the node module (docs/modules/console-log.md) or turn this variable off")
		}
	}
	if env.flag("ENABLE_GUARD") {
		cfg.Guard.Action = OverrideString(env.Getenv("GUARD_ACTION"), cfg.Guard.Action)
		cfg.Guard.BanMinutes = OverrideInt(env.Getenv("GUARD_BAN_MINUTES"), cfg.Guard.BanMinutes)
		cfg.Guard.WhitelistSteamIDs = OverrideList(env.Getenv("GUARD_WHITELIST_STEAMIDS"), cfg.Guard.WhitelistSteamIDs)
		cfg.Guard.WhitelistIPs = OverrideList(env.Getenv("GUARD_WHITELIST_IPS"), cfg.Guard.WhitelistIPs)
		g, err = guard.New(cfg.Guard, log)
		if err != nil {
			log.Logf(logx.Error, "Bot-guard config invalid: %v", err)
			g = nil
		} else {
			g.Report = link.send
			g.NodeActive = func() bool { return link.hasModule("guard") }
			log.Logf(logx.Success, "Bot-guard active: %d rules, action=%s, ban=%dm", g.RuleCount(), g.Action, g.BanMinutes)
			if link.hasModule("guard") {
				log.Log(logx.Debug, "Host guard on the node reports its own blocks in this console")
			}
		}
	}

	cmdline := startup.Expand(env.Getenv("STARTUP"), env.Getenv)
	log.Logf(logx.Info, "Starting server: %s", cmdline)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.Dir = env.Root
	cmd.Env = os.Environ()
	if dbg := startup.Debugger(env.Getenv("GDB_DEBUG_PORT")); dbg != "" {
		cmd.Env = append(cmd.Env, "GAME_DEBUGGER="+dbg)
		log.Logf(logx.Info, "GDB mode: Server will start under gdbserver on port %s", env.Getenv("GDB_DEBUG_PORT"))
		log.Log(logx.Warning, "Server will wait for debugger connection before starting")
	}
	cmd.Cancel = func() error { return nil } // the supervisor stops it gracefully
	srv := &supervisor.Server{Cmd: cmd, Stdin: env.Stdin, StopCommand: "quit", Grace: 30 * time.Second}
	pipeline.Stopping = srv.Stopping

	// the egg's own picture of the server, reported to the node on change,
	// on every (re)connect, and on a heartbeat so a node that restarted
	// picks the map and player count back up without waiting for one
	w := watch.New(500)
	sendState := func() { st := w.State(); link.send(&st) }
	link.onConnect = sendState
	sendState()
	go func() {
		t := time.NewTicker(StateHeartbeat)
		defer t.Stop()
		var lastDropped int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sendState()
				if nodeSink != nil {
					if n := nodeSink.Dropped(); n > lastDropped {
						log.Logf(logx.Warning, "Console log forwarding fell behind, %d line(s) dropped since last check", n-lastDropped)
						lastDropped = n
					}
				}
			}
		}
	}()
	var crashOnce sync.Once
	reportCrash := func(code int) {
		crashOnce.Do(func() {
			st := w.State()
			link.send(&proto.Crash{ExitCode: code, Lines: w.Recent(proto.MaxLine / 4), Map: st.Map, Players: st.Players})
			time.Sleep(300 * time.Millisecond) // let the node read it before the container is gone
		})
	}
	pipeline.OnCrash = func(string) { reportCrash(134) }
	// the server name players see is a convar, so the only way to know it
	// is to ask: once the engine is live, and again after a map change,
	// because a plugin may rewrite it per map
	if w.SetName(hostnameFromConfig(env.Root)) {
		sendState()
	}
	ask := &nameQuery{send: func(c string) { srv.Send(c) }}
	// a typed command comes back as a bare echo with no level and no file
	// entry; this turns it into a real log line and drops the echo
	typed := &inputEcho{}
	srv.OnInput = func(cmd string) {
		typed.expect(cmd)
		log.Log(logx.Input, log.Mask(cmd))
	}
	srv.Line = func(line string) {
		// the ring is what a crash report carries out of the container, so
		// it holds the masked line: the terminal echoes whatever is typed
		// into it, and an operator setting rcon_password in the panel
		// would otherwise ship it to the node in the next crash bundle
		if w.Feed(log.Mask(line)) {
			sendState()
		}
		if !ask.handle(line) || typed.swallow(line) {
			return // our own answer or the echo we already printed
		}
		if g == nil || g.Inspect(line) {
			pipeline.Handle(line)
		}
	}
	if g != nil {
		g.Send = func(c string) { srv.Send(c) }
	}
	// node traffic for the whole run: block notices to the guard, console
	// commands to the server
	go link.receive(ctx, func(m proto.Message) {
		switch x := m.(type) {
		case *proto.GuardBlock:
			if g != nil {
				g.HostNotice(x)
			}
		case *proto.ConsoleExec:
			srv.Send(x.Cmd)
		}
	})
	err = srv.Run(ctx)
	w.SetUp(false)
	log.Log(logx.Info, "Server stopped")
	var exitErr *exec.ExitError
	code := 0
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		log.Logf(logx.Error, "Server run failed: %v", err)
		code = 1
	}
	if code != 0 && !srv.Stopping() {
		reportCrash(code)
	}
	sendState()
	return code
}

// NodeConnectWindow is how long the egg waits for the node's socket.
var NodeConnectWindow = 20 * time.Second

// WorkshopTimeout bounds the node's workshop pass: it may have to move a
// few GB of maps between the volume and its store.
var WorkshopTimeout = 5 * time.Minute

// syncWorkshop lets the node reshape the workshop content before the server
// starts: its copies move into the node's store and come back as links, and
// maps another server already downloaded arrive for free.
func syncWorkshop(node *nodeclient.Client, log *logx.Console) {
	log.Log(logx.Running, "Syncing workshop content with the node...")
	msg, err := node.Request(&proto.WorkshopSync{}, WorkshopTimeout, func(m proto.Message) bool {
		_, ok := m.(*proto.WorkshopReady)
		return ok
	})
	if err != nil {
		log.Logf(logx.Warning, "Node did not finish the workshop sync (%v) - the server keeps its own copies", err)
		return
	}
	r := msg.(*proto.WorkshopReady)
	switch {
	case r.Err != "":
		log.Logf(logx.Warning, "Workshop sync failed on the node: %s", r.Err)
	case r.Absorbed+r.Linked+r.Seeded+r.Released == 0:
		log.Log(logx.Debug, "Workshop content already in sync with the node")
	default:
		log.Logf(logx.Success, "Workshop: %d shared, %d received, %d released%s",
			r.Absorbed+r.Linked, r.Seeded, r.Released, freedNote(r.Freed))
	}
}

func freedNote(bytes int64) string {
	if bytes <= 0 {
		return ""
	}
	return fmt.Sprintf(", %.1f GB freed in this server", float64(bytes)/1e9)
}

// AddonCacheTimeout bounds one addon query: the node may have to download
// the file first.
var AddonCacheTimeout = 5 * time.Minute

// StateHeartbeat is how often the egg restates map and players even when
// nothing changed.
var StateHeartbeat = 30 * time.Second

func nodeHello(env Env) proto.Hello {
	return proto.Hello{BootID: fmt.Sprintf("%d", time.Now().Unix()), Guard: &proto.GuardInfo{Enabled: env.flag("ENABLE_GUARD"), Action: "log"}}
}

// nodeLink holds the node connection for the run and reconnects when the
// node restarts, so guard traffic survives a daemon update.
type nodeLink struct {
	mu     sync.Mutex
	client *nodeclient.Client
	path   string
	hello  proto.Hello
	// onConnect runs after every (re)connect, so the egg re-states what the
	// node lost when it restarted.
	onConnect func()
}

func (l *nodeLink) send(m proto.Message) {
	l.mu.Lock()
	c := l.client
	l.mu.Unlock()
	if c != nil {
		c.Send(m)
	}
}

func (l *nodeLink) hasModule(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.client != nil && l.client.HasModule(name)
}

// receive dispatches node messages and reconnects on loss.
func (l *nodeLink) receive(ctx context.Context, handle func(proto.Message)) {
	for ctx.Err() == nil {
		l.mu.Lock()
		c := l.client
		l.mu.Unlock()
		if c == nil {
			nc, err := nodeclient.Connect(ctx, l.path, 5*time.Second, l.hello)
			if err != nil {
				continue // Connect already waited its window
			}
			l.mu.Lock()
			l.client = nc
			l.mu.Unlock()
			if l.onConnect != nil {
				l.onConnect()
			}
			continue
		}
		msg, err := c.Next()
		if err != nil {
			c.Close()
			l.mu.Lock()
			if l.client == c {
				l.client = nil
			}
			l.mu.Unlock()
			continue
		}
		handle(msg)
	}
}

// handshake asks the node daemon (if any) whether it manages the VPK files.
// The connection is kept for the guard; nil when there is no node.
func handshake(ctx context.Context, env Env, eggDir string, log *logx.Console) (*nodeclient.Client, bool) {
	cl, err := nodeclient.Connect(ctx, filepath.Join(eggDir, nodeclient.SocketName), NodeConnectWindow, nodeHello(env))
	if err != nil {
		if !errors.Is(err, nodeclient.ErrNoNode) && ctx.Err() == nil {
			log.Logf(logx.Warning, "Node daemon handshake failed: %v", err)
		}
		return nil, false
	}
	log.Logf(logx.Debug, "Connected to cs2node %s (modules: %s)", cl.Node.Version, strings.Join(cl.Node.Modules, ", "))
	if !cl.HasModule("vpksync") {
		return cl, false
	}
	log.Log(logx.Running, "Checking for daemon-managed game files...")
	v, err := cl.AwaitVerdict(ctx, func(state string, pos int) {
		switch state {
		case proto.StateQueued:
			if pos > 1 {
				log.Logf(logx.Running, "Waiting for the node: %d server(s) ahead in queue", pos-1)
			} else {
				log.Log(logx.Running, "Waiting for the node to verify game files...")
			}
		case proto.StateUpdating:
			log.Log(logx.Running, "Central CS2 update in progress, waiting...")
		case proto.StateVerifying:
			log.Log(logx.Running, "Node is verifying game files...")
		case proto.StatePushing:
			log.Log(logx.Running, "Node is pushing game files...")
		}
	})
	switch {
	case err != nil:
		if ctx.Err() == nil {
			log.Logf(logx.Warning, "Node daemon connection lost: %v - falling back to SteamCMD", err)
		}
		cl.Close()
		return nil, false
	case v.Failed:
		log.Code(logx.Warning, "KL-DMN-03", "Node daemon reported a failed VPK push - falling back to SteamCMD")
		return cl, false
	case v.Managed:
		if !hasResolvableVPK(filepath.Join(env.Root, "game", "csgo")) {
			log.Code(logx.Warning, "KL-DMN-04", "Node daemon said done but no VPK file is readable - falling back to SteamCMD")
			return cl, false
		}
		log.Log(logx.Success, "Daemon-managed game files, SteamCMD skipped")
		return cl, true
	}
	return cl, false
}

func hasResolvableVPK(csgo string) bool {
	found := false
	filepath.WalkDir(csgo, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if strings.Count(strings.TrimPrefix(path, csgo), string(os.PathSeparator)) > 3 {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".vpk") {
			if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// removeStale drops the local SteamCMD tree when the node owns the files.
func removeStale(root string, log *logx.Console) {
	var existing []string
	for _, d := range []string{"steamcmd", "Steam", "steamapps"} {
		p := filepath.Join(root, d)
		if _, err := os.Lstat(p); err == nil {
			existing = append(existing, p)
		}
	}
	if len(existing) == 0 {
		return
	}
	log.Logf(logx.Info, "Daemon mode active - removing %d stale artifact(s)", len(existing))
	for _, p := range existing {
		os.RemoveAll(p)
	}
}

// pruneBrokenVPKLinks removes dangling *.vpk symlinks a lost daemon setup
// left behind; SteamCMD chokes on them.
func pruneBrokenVPKLinks(gameDir string, log *logx.Console) {
	removed := 0
	filepath.WalkDir(gameDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 || !strings.HasSuffix(d.Name(), ".vpk") {
			return nil
		}
		if _, err := os.Stat(path); err != nil {
			if os.Remove(path) == nil {
				removed++
			}
		}
		return nil
	})
	if removed > 0 {
		log.Logf(logx.Warning, "Removed %d broken VPK symlink(s) left by a previous daemon setup", removed)
	}
}

func runCleanup(root string, cfg configs.Cleanup, log *logx.Console) {
	if len(cfg.Rules) == 0 {
		log.Log(logx.Debug, "No cleanup rules defined in cleanup.json")
		return
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		log.Log(logx.Error, "Cleanup skipped: "+err.Error())
		return
	}
	defer r.Close()
	start := time.Now()
	res := cleanup.Run(r, cfg.Rules, cleanup.Options{Now: start, Bases: []string{root}})
	for _, e := range res.Errors {
		log.Log(logx.Error, e)
	}
	if res.Files > 0 {
		log.Logf(logx.Success, "Cleaned up %d file(s), freed %s in %ds", res.Files, cleanup.FormatSize(res.Bytes), int(time.Since(start).Seconds()))
		for name, s := range res.PerRule {
			if s.Files > 0 {
				log.Logf(logx.Debug, "  %s: %d file(s), %s", name, s.Files, cleanup.FormatSize(s.Bytes))
			}
		}
	}
}

// selection maps the panel booleans (and the legacy ADDON_SELECTION string).
func selection(env Env) addons.Selection {
	sel := addons.Selection{Metamod: env.flag("INSTALL_METAMOD"), CSS: env.flag("INSTALL_CSS"), Swiftly: env.flag("INSTALL_SWIFTLY"), ModSharp: env.flag("INSTALL_MODSHARP")}
	switch env.Getenv("ADDON_SELECTION") {
	case "Metamod Only":
		sel.Metamod = true
	case "Metamod + CounterStrikeSharp":
		sel.Metamod, sel.CSS = true, true
	case "SwiftlyS2":
		sel.Swiftly = true
	case "ModSharp":
		sel.ModSharp = true
	}
	return sel
}

// Describe is the one-line identity for --version style output.
func Describe() string {
	return fmt.Sprintf("cs2egg %s (%s, %s, built %s)", version.Version, version.Channel, version.Commit, version.Built)
}

// hostnameFromConfig reads the name out of server.cfg, which covers the
// boot before the engine answers and every server that never changes it.
func hostnameFromConfig(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "game", "csgo", "cfg", "server.cfg"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "hostname") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "hostname"))
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		return strings.Trim(rest, `"`)
	}
	return ""
}

// nameQuery types `hostname` into the console when the engine comes up and
// after every map change, and swallows the one answer it asked for so the
// panel console does not grow a line per map. An answer to a `hostname` the
// operator typed is left alone.
type nameQuery struct {
	send    func(string)
	pending bool
	asked   bool
}

// handle reports whether the line should go on to the console.
func (q *nameQuery) handle(line string) bool {
	if q.pending && watch.IsHostnameReply(line) {
		q.pending = false
		return false
	}
	switch {
	case strings.Contains(line, "Host activate"):
		// every map load, because a plugin may set the name per map
		q.asked, q.pending = true, true
		q.send("hostname")
	case !q.asked && strings.Contains(line, "hibernating"):
		// a server that came up straight into hibernation still has a name
		q.asked, q.pending = true, true
		q.send("hostname")
	}
	return true
}

// inputEcho drops the terminal's echo of a command the operator typed,
// because the egg has already printed it as a proper log line. Only the
// very next console line counts, so a later line that happens to read the
// same is left alone.
type inputEcho struct {
	mu   sync.Mutex
	last string
}

func (e *inputEcho) expect(cmd string) {
	e.mu.Lock()
	e.last = cmd
	e.mu.Unlock()
}

// swallow reports whether the line is the echo we are waiting for.
func (e *inputEcho) swallow(line string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	want := e.last
	e.last = ""
	return want != "" && strings.TrimRight(line, "\r\n") == want
}
