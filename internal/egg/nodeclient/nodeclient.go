// SPDX-License-Identifier: GPL-3.0-or-later

// Package nodeclient is the egg's side of the unix socket the node opens in
// the server volume. Boot asks it for the VPK verdict; the guard uses it for
// the rest of the run.
package nodeclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
	"github.com/K4ryuu/CS2-Egg-Go/internal/version"
)

// SocketName inside egg/.
const SocketName = "cs2node.sock"

// ErrNoNode: nothing answered on the socket within the connect window.
var ErrNoNode = errors.New("no node daemon on this volume")

// Client is one connection to the node.
type Client struct {
	conn net.Conn
	r    *proto.Reader
	wmu  sync.Mutex
	Node proto.Hello // the node's hello
}

// Connect dials the socket, retrying until timeout, and exchanges hellos.
func Connect(ctx context.Context, path string, timeout time.Duration, hello proto.Hello) (*Client, error) {
	deadline := time.Now().Add(timeout)
	var conn net.Conn
	for {
		var err error
		conn, err = net.DialTimeout("unix", path, 2*time.Second)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, ErrNoNode
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	c := &Client{conn: conn, r: proto.NewReader(conn)}
	if hello.Version == "" {
		hello.Version = version.Version
	}
	if hello.Built == "" {
		hello.Built = version.Built
	}
	if err := c.Send(&hello); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	first, err := c.r.Next()
	conn.SetReadDeadline(time.Time{})
	h, ok := first.(*proto.Hello)
	if err != nil || !ok {
		conn.Close()
		return nil, fmt.Errorf("node did not answer the hello: %v", err)
	}
	c.Node = *h
	return c, nil
}

// HasModule reports whether the node advertised a module.
func (c *Client) HasModule(name string) bool { return slices.Contains(c.Node.Modules, name) }

// Send writes one message to the node.
func (c *Client) Send(msg proto.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return proto.Encode(c.conn, msg)
}

// Next blocks for the next message from the node.
func (c *Client) Next() (proto.Message, error) { return c.r.Next() }

// Request sends msg and waits up to timeout for the first reply match
// accepts; other messages arriving meanwhile are dropped. Boot-time use
// only, before the notice reader runs.
func (c *Client) Request(msg proto.Message, timeout time.Duration, match func(proto.Message) bool) (proto.Message, error) {
	if err := c.Send(msg); err != nil {
		return nil, err
	}
	c.conn.SetReadDeadline(time.Now().Add(timeout))
	defer c.conn.SetReadDeadline(time.Time{})
	for {
		m, err := c.r.Next()
		if err != nil {
			// the scanner is done after an error: drop the connection so the
			// run-time reader reconnects instead of reading a dead stream
			c.conn.Close()
			return nil, err
		}
		if match(m) {
			return m, nil
		}
	}
}

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Verdict is the outcome of the VPK handshake.
type Verdict struct {
	Managed bool // the node owns the VPK files, skip SteamCMD
	Failed  bool // the node said failed: fall back to SteamCMD
}

// AwaitVerdict reads vpk.status messages until a terminal one. progress is
// told about every state change (state, queue position).
func (c *Client) AwaitVerdict(ctx context.Context, progress func(state string, queuePos int)) (Verdict, error) {
	if !c.HasModule("vpksync") {
		return Verdict{}, nil
	}
	last := ""
	for {
		if ctx.Err() != nil {
			return Verdict{}, ctx.Err()
		}
		msg, err := c.Next()
		if err != nil {
			return Verdict{}, fmt.Errorf("node connection lost before a verdict: %w", err)
		}
		st, ok := msg.(*proto.VpkStatus)
		if !ok {
			continue
		}
		switch st.State {
		case proto.StateDone:
			return Verdict{Managed: true}, nil
		case proto.StateFailed:
			return Verdict{Failed: true}, nil
		}
		key := fmt.Sprintf("%s/%d", st.State, st.QueuePos)
		if key != last && progress != nil {
			progress(st.State, st.QueuePos)
		}
		last = key
	}
}
