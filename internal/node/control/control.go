// SPDX-License-Identifier: GPL-3.0-or-later

// Package control is the root-only unix socket the running daemon answers
// status queries on, so `cs2node status` and `doctor` see live state
// without touching the volume sockets.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"
)

// DefaultPath of the control socket.
const DefaultPath = "/run/cs2node/control.sock"

// Serve answers every connection with one JSON document from snapshot.
func Serve(ctx context.Context, path string, snapshot func() any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	go func() {
		<-ctx.Done()
		ln.Close()
		os.Remove(path)
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			json.NewEncoder(conn).Encode(snapshot())
		}()
	}
}

// ErrNoDaemon: nothing listens on the control socket.
var ErrNoDaemon = errors.New("cs2node daemon is not running")

// Query fetches the snapshot into v.
func Query(path string, v any) error {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return ErrNoDaemon
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	return json.NewDecoder(conn).Decode(v)
}
