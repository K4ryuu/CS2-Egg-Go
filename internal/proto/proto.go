// SPDX-License-Identifier: GPL-3.0-or-later

// Package proto is the egg<->node wire format: one JSON object per line over
// the unix socket the node opens inside each server volume. Every message
// type lives here and nowhere else.
package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version of the envelope. Bump when a message changes shape incompatibly.
const Version = 1

// Type names a message.
type Type string

const (
	TypeHello         Type = "hello"
	TypeVpkStatus     Type = "vpk.status"
	TypeGuardBlock    Type = "guard.block"
	TypeGuardHit      Type = "guard.hit"
	TypeGuardEvent    Type = "guard.ev"
	TypeServerState   Type = "server.state"
	TypeConsoleExec   Type = "console.exec"
	TypeCrash         Type = "egg.crash"
	TypeAddonQuery    Type = "addon.query"
	TypeAddonRelease  Type = "addon.release"
	TypeWorkshopSync  Type = "workshop.sync"
	TypeWorkshopReady Type = "workshop.ready"
	TypeLogLine       Type = "log.line"
)

// vpk.status states, in the order a push goes through them
const (
	StateQueued    = "queued"
	StateUpdating  = "updating"
	StateVerifying = "verifying"
	StatePushing   = "pushing"
	StateDone      = "done"
	StateFailed    = "failed"
)

// guard.ev kinds
const (
	EventJoin  = "join"
	EventLeave = "leave"
	EventChat  = "chat"
)

// Message is any payload that knows its type.
type Message interface{ Type() Type }

// Hello opens a connection from either side. The node fills Version and
// Modules, the egg fills Version, BootID and Guard.
type Hello struct {
	Version string     `json:"version"`
	Built   string     `json:"built,omitempty"` // build stamp, so a node can tell an old image
	Modules []string   `json:"modules,omitempty"`
	BootID  string     `json:"boot_id,omitempty"`
	Guard   *GuardInfo `json:"guard,omitempty"`
}

// GuardInfo is the egg guard's mode, so the node knows whether kicks land.
type GuardInfo struct {
	Enabled bool   `json:"enabled"`
	Action  string `json:"action"` // log | kick | block
}

// VpkStatus is the node's progress on this container's VPK files.
type VpkStatus struct {
	State    string `json:"state"`
	QueuePos int    `json:"queue_pos,omitempty"`
}

// GuardBlock tells the egg the node dropped (or, Dry, would drop) an address
// for this server. UID lets the egg kick the client right away.
type GuardBlock struct {
	IP      string `json:"ip"`
	Minutes int    `json:"minutes"`
	Rule    string `json:"rule"`
	Src     string `json:"src"` // host | egg
	UID     int    `json:"uid,omitempty"`
	Dry     bool   `json:"dry,omitempty"`
}

// GuardHit is an egg rule trip the node turns into a packet drop.
type GuardHit struct {
	IP      string `json:"ip"`
	Minutes int    `json:"minutes"`
	Rule    string `json:"rule"`
	UID     int    `json:"uid,omitempty"`
}

// GuardEvent is a client lifecycle fact the node's heuristics run on.
type GuardEvent struct {
	Kind string `json:"kind"` // join | leave | chat
	IP   string `json:"ip"`
	UID  int    `json:"uid,omitempty"`
}

// ServerState is what the egg sees of the running server: sent on every
// change (map load, join, leave) and right after hello.
type ServerState struct {
	Map     string `json:"map"`
	Name    string `json:"name,omitempty"` // the hostname players see
	Players int    `json:"players"`
	Up      bool   `json:"up"` // the server process is running
}

// ConsoleExec asks the egg to type one command into the server console.
type ConsoleExec struct {
	Cmd string `json:"cmd"`
}

// Crash carries the console tail of a real crash (not a stop) to the node.
type Crash struct {
	ExitCode int      `json:"exit_code"`
	Lines    []string `json:"lines"`
	Map      string   `json:"map,omitempty"`
	Players  int      `json:"players,omitempty"`
}

// AddonQuery asks the node's addon cache either for the current release of
// a GitHub repo (Repo set: the answer lists the assets with their URLs) or
// for one file (URL set: the node delivers it into the volume's egg dir).
type AddonQuery struct {
	Repo       string `json:"repo,omitempty"`
	URL        string `json:"url,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

// AddonAsset is one release file. Path is set once delivered, relative to
// the volume's egg dir.
type AddonAsset struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
}

// AddonRelease answers an AddonQuery (Repo and URL echo the query). Err set
// = the egg falls back to GitHub, unless Pinned is set too: a pin the node
// cannot resolve must not turn into "install the newest".
type AddonRelease struct {
	// Pinned: the node picked this release by version, so the egg installs
	// it even when that means going back.
	Pinned     bool         `json:"pinned,omitempty"`
	Repo       string       `json:"repo,omitempty"`
	URL        string       `json:"url,omitempty"`
	Tag        string       `json:"tag,omitempty"`
	Prerelease bool         `json:"prerelease,omitempty"`
	Assets     []AddonAsset `json:"assets,omitempty"`
	Err        string       `json:"err,omitempty"`
}

// LogLine is one console line for the node's console-log module to persist
// to host disk: the egg's own messages (Kind is a level name) and the game
// server's own output (Kind is "server") alike, exactly what the panel's
// console tab shows.
type LogLine struct {
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
}

// WorkshopSync asks the node to bring this volume in line with its workshop
// cache. Sent at boot, before the server process starts.
type WorkshopSync struct{}

// WorkshopReady answers a WorkshopSync with what the node did.
type WorkshopReady struct {
	Absorbed int    `json:"absorbed,omitempty"` // moved into the node's store
	Linked   int    `json:"linked,omitempty"`   // a copy the node already had
	Seeded   int    `json:"seeded,omitempty"`   // handed over without a download
	Released int    `json:"released,omitempty"` // given back for the server to update
	Freed    int64  `json:"freed,omitempty"`    // bytes the volume no longer holds
	Err      string `json:"err,omitempty"`
}

func (*WorkshopSync) Type() Type  { return TypeWorkshopSync }
func (*WorkshopReady) Type() Type { return TypeWorkshopReady }

func (*ServerState) Type() Type  { return TypeServerState }
func (*ConsoleExec) Type() Type  { return TypeConsoleExec }
func (*Crash) Type() Type        { return TypeCrash }
func (*LogLine) Type() Type      { return TypeLogLine }
func (*AddonQuery) Type() Type   { return TypeAddonQuery }
func (*AddonRelease) Type() Type { return TypeAddonRelease }

func (*Hello) Type() Type      { return TypeHello }
func (*VpkStatus) Type() Type  { return TypeVpkStatus }
func (*GuardBlock) Type() Type { return TypeGuardBlock }
func (*GuardHit) Type() Type   { return TypeGuardHit }
func (*GuardEvent) Type() Type { return TypeGuardEvent }

type envelope struct {
	T Type            `json:"t"`
	V int             `json:"v"`
	D json.RawMessage `json:"d"`
}

// Encode writes m as one line.
func Encode(w io.Writer, m Message) error {
	d, err := json.Marshal(m)
	if err != nil {
		return err
	}
	line, err := json.Marshal(envelope{T: m.Type(), V: Version, D: d})
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}

type unknownType struct{ t Type }

func (e unknownType) Error() string { return fmt.Sprintf("unknown message type %q", e.t) }

// IsUnknown reports whether err came from a message type this build does not
// know. Callers skip those instead of dropping the connection.
func IsUnknown(err error) bool {
	var u unknownType
	return errors.As(err, &u)
}

// Decode parses one line.
func Decode(line []byte) (Message, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	var m Message
	switch env.T {
	case TypeHello:
		m = &Hello{}
	case TypeVpkStatus:
		m = &VpkStatus{}
	case TypeGuardBlock:
		m = &GuardBlock{}
	case TypeGuardHit:
		m = &GuardHit{}
	case TypeGuardEvent:
		m = &GuardEvent{}
	case TypeServerState:
		m = &ServerState{}
	case TypeConsoleExec:
		m = &ConsoleExec{}
	case TypeCrash:
		m = &Crash{}
	case TypeLogLine:
		m = &LogLine{}
	case TypeAddonQuery:
		m = &AddonQuery{}
	case TypeAddonRelease:
		m = &AddonRelease{}
	case TypeWorkshopSync:
		m = &WorkshopSync{}
	case TypeWorkshopReady:
		m = &WorkshopReady{}
	default:
		return nil, unknownType{env.T}
	}
	if err := json.Unmarshal(env.D, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Reader yields messages from a line stream, skipping unknown types and
// blank lines. Any other error ends the stream.
type Reader struct{ s *bufio.Scanner }

// MaxLine bounds one message: a crash report carries up to 500 console
// lines, everything else is tiny.
const MaxLine = 1 << 20

// NewReader wraps r. Lines longer than MaxLine end the stream.
func NewReader(r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), MaxLine)
	return &Reader{s: s}
}

// Next returns the next known message or an error (io.EOF at the end).
func (r *Reader) Next() (Message, error) {
	for r.s.Scan() {
		line := r.s.Bytes()
		if len(line) == 0 {
			continue
		}
		m, err := Decode(line)
		if IsUnknown(err) {
			continue
		}
		return m, err
	}
	if err := r.s.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}
