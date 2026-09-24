// SPDX-License-Identifier: GPL-3.0-or-later

package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ContainerStatus is one container as reported over the control socket.
type ContainerStatus struct {
	Name      string   `json:"name"`
	Image     string   `json:"image"`
	Volume    string   `json:"volume"`
	Ports     []uint16 `json:"ports"`
	Connected bool     `json:"connected"`
	EggBuilt  string   `json:"egg_built,omitempty"` // the connected egg's build stamp
	Reported  bool     `json:"reported"`            // the egg sent a server.state
	Map       string   `json:"map,omitempty"`
	Server    string   `json:"server,omitempty"` // the hostname players see
	Players   int      `json:"players"`
	Up        bool     `json:"up"`
}

// Status is the daemon's snapshot for status and doctor commands.
type Status struct {
	Version    string                     `json:"version"`
	Built      string                     `json:"built,omitempty"` // build stamp of the running daemon
	Modules    []string                   `json:"modules"`
	Containers []ContainerStatus          `json:"containers"`
	Module     map[string]json.RawMessage `json:"module"` // per-module extras
}

var providersMu sync.Mutex

// ProvideStatus lets a module add its own section to Status.
func (c *Core) ProvideStatus(name string, fn func() any) {
	providersMu.Lock()
	defer providersMu.Unlock()
	if c.providers == nil {
		c.providers = map[string]func() any{}
	}
	c.providers[name] = fn
}

// Status builds the live snapshot (daemon side).
func (c *Core) Status(version, built string) Status {
	st := Status{Version: version, Built: built, Modules: c.modules, Module: map[string]json.RawMessage{}}
	c.mu.Lock()
	for _, e := range c.containers {
		st.Containers = append(st.Containers, ContainerStatus{
			Name: e.c.Name, Image: e.c.Image, Volume: e.c.Volume, Ports: e.c.Ports,
			Connected: e.sock != nil && e.sock.connected(), EggBuilt: e.egg.Built,
			Reported: e.reported, Map: e.state.Map, Server: e.state.Name, Players: e.state.Players, Up: e.state.Up,
		})
	}
	c.mu.Unlock()
	sort.Slice(st.Containers, func(i, j int) bool { return st.Containers[i].Name < st.Containers[j].Name })
	providersMu.Lock()
	for name, fn := range c.providers {
		if raw, err := json.Marshal(fn()); err == nil {
			st.Module[name] = raw
		}
	}
	providersMu.Unlock()
	return st
}

// UseRemote makes a passive core answer Connected and ModuleStatus from a
// snapshot fetched over the control socket.
func (c *Core) UseRemote(st Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remote = &st
}

// ModuleStatus returns a module's section of the remote snapshot.
func (c *Core) ModuleStatus(name string, v any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remote == nil {
		return false
	}
	raw, ok := c.remote.Module[name]
	return ok && json.Unmarshal(raw, v) == nil
}

// PickName resolves what the operator typed to one server: an exact match
// wins, otherwise the single name it is a prefix of. Printing short ids is
// only honest if they can be typed back, and that is what this makes true.
func PickName(names []string, typed string) (string, error) {
	if typed == "" {
		return "", nil
	}
	var hits []string
	for _, n := range names {
		if n == typed {
			return n, nil
		}
		if strings.HasPrefix(n, typed) {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no server here is called %s", typed)
	case 1:
		return hits[0], nil
	}
	sort.Strings(hits)
	return "", fmt.Errorf("%s matches %d servers (%s): type more of it", typed, len(hits), strings.Join(hits, ", "))
}

// Names is the container names of a snapshot.
func Names(conts []Container) []string {
	out := make([]string, 0, len(conts))
	for _, c := range conts {
		out = append(out, c.Name)
	}
	return out
}
