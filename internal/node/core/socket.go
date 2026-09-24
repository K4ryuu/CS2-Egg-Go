// SPDX-License-Identifier: GPL-3.0-or-later

package core

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// SocketName is the file inside <volume>/egg the egg connects to.
const SocketName = "cs2node.sock"

// SocketPath is the host-side path for a container's socket.
func SocketPath(volume string) string { return filepath.Join(volume, "egg", SocketName) }

// sockServer is one container's listener. One egg connection at a time; a
// new connection replaces the previous one (the egg reconnects on its own).
type sockServer struct {
	core      *Core
	container Container
	path      string
	ln        net.Listener
	dir       *os.File // egg/, held open while listening

	mu   sync.Mutex
	conn net.Conn
	wmu  sync.Mutex
}

// openSocket listens inside the volume. egg/ is created if missing and
// handed to the volume owner, so the egg keeps owning its directory.
//
// The volume belongs to the container user, who can turn any path in it
// into a symlink; every step goes through os.Root so nothing the daemon
// does as root can be steered outside the volume.
func (c *Core) openSocket(cont Container) *sockServer {
	root, err := os.OpenRoot(cont.Volume)
	if err != nil {
		c.log.Warn("cannot open volume", "container", cont.Name, "err", err)
		return nil
	}
	defer root.Close()
	if err := root.MkdirAll("egg", 0o755); err != nil {
		c.log.Warn("cannot create egg dir (a symlink in the way?)", "container", cont.Name, "err", err)
		return nil
	}
	root.Lchown("egg", cont.Owner.UID, cont.Owner.GID)
	dir, err := root.Open("egg")
	if err != nil {
		c.log.Warn("cannot open egg dir", "container", cont.Name, "err", err)
		return nil
	}
	rel := "egg/" + SocketName
	root.Remove(rel)
	// bind through the open directory, so a swapped egg/ cannot move the
	// socket elsewhere between the checks and the bind (and the 108-byte
	// sun_path limit never bites on a deep volume path)
	ln, err := net.Listen("unix", listenPath(dir, SocketName))
	if err != nil {
		dir.Close()
		c.log.Warn("cannot listen on volume socket", "container", cont.Name, "path", SocketPath(cont.Volume), "err", err)
		return nil
	}
	root.Lchown(rel, cont.Owner.UID, cont.Owner.GID)
	root.Chmod(rel, 0o660)
	s := &sockServer{core: c, container: cont, path: SocketPath(cont.Volume), ln: ln, dir: dir}
	go s.acceptLoop()
	return s
}

func (s *sockServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.serve(conn)
	}
}

func (s *sockServer) serve(conn net.Conn) {
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
	}
	s.conn = conn
	s.mu.Unlock()

	// an egg that connects and says nothing must not hold the slot open
	conn.SetReadDeadline(time.Now().Add(HelloTimeout))
	r := proto.NewReader(conn)
	first, err := r.Next()
	hello, ok := first.(*proto.Hello)
	if err != nil || !ok {
		s.core.log.Warn("egg connection without hello", "container", s.container.Name, "err", err)
		s.drop(conn)
		return
	}
	if err := s.send(&proto.Hello{Version: version.Version, Modules: s.core.modules}); err != nil {
		s.drop(conn)
		return
	}
	conn.SetReadDeadline(time.Time{}) // the connection is live, let it idle
	s.core.setEgg(s.container.Name, *hello)
	s.core.log.Info("egg connected", "container", s.container.Name, "egg", hello.Version, "built", hello.Built, "boot", hello.BootID)
	s.core.emit(Event{Kind: EggConnected, Container: s.container})
	for {
		msg, err := r.Next()
		if err != nil {
			break
		}
		s.core.dispatch(s.container.Name, msg)
	}
	if s.drop(conn) {
		s.core.log.Info("egg disconnected", "container", s.container.Name)
		s.core.emit(Event{Kind: EggGone, Container: s.container})
	}
}

// Timeouts on the volume socket. A container is not trusted to be alive or
// to keep reading: the daemon is root and serves every other server on the
// node, so nothing it does for one container may block on that container.
const (
	HelloTimeout = 30 * time.Second // from accept to the egg's hello
	WriteTimeout = 10 * time.Second // for one message to reach the egg
)

// drop closes conn and forgets it if it is still the current one.
func (s *sockServer) drop(conn net.Conn) bool {
	conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == conn {
		s.conn = nil
		return true
	}
	return false
}

func (s *sockServer) connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

func (s *sockServer) send(msg proto.Message) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	// Without a deadline, a container that stops reading blocks this write
	// forever while holding wmu, and then every other writer to it blocks
	// too: the single /dev/kmsg reader that feeds block notices to EVERY
	// server on the node, the idle watcher, the vpksync announcer. One
	// container could stall the daemon for all of them.
	if err := conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return err
	}
	err := proto.Encode(conn, msg)
	if err != nil {
		// a half-written message leaves the stream out of frame, and the
		// egg reconnects on its own
		s.drop(conn)
	}
	return err
}

func (s *sockServer) close() {
	s.ln.Close() // unlinks the socket through the still-open dir
	s.mu.Lock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()
	s.dir.Close()
}
