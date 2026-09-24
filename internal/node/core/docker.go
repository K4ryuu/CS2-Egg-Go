// SPDX-License-Identifier: GPL-3.0-or-later

package core

import (
	"context"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Container is what the node knows about one game server.
type Container struct {
	ID     string
	Name   string // == the panel's server uuid under Wings
	Image  string
	Volume string // host path mounted at /home/container
	Owner  Owner
	Ports  []uint16 // every published host port, tcp and udp alike
	PID    int
}

// Owner is the volume root's uid:gid; Wings and Pelican differ, never guess.
type Owner struct{ UID, GID int }

func (o Owner) String() string { return strconv.Itoa(o.UID) + ":" + strconv.Itoa(o.GID) }

// EventKind is what happened to a container.
type EventKind int

const (
	Start        EventKind = iota // container running (also emitted on reconcile)
	Die                           // container gone
	EggConnected                  // the egg opened its socket connection
	EggGone                       // the egg's connection dropped
)

func (k EventKind) String() string {
	return [...]string{"start", "die", "egg-connected", "egg-gone"}[k]
}

// Event is delivered to subscribers.
type Event struct {
	Kind      EventKind
	Container Container
	// ExitCode is what the process left behind on a Die event. Docker
	// reports it as a string attribute; -1 means it did not say.
	ExitCode int
}

// Docker is the slice of the engine API the core needs; tests fake it.
type Docker interface {
	// Running lists running containers whose image matches one of images.
	Running(ctx context.Context, images []string) ([]Container, error)
	// Inspect fills in volume, ports and pid for one container.
	Inspect(ctx context.Context, id string) (Container, error)
	// Events streams start/die for containers of the given images until ctx
	// ends; the error channel reports a broken stream.
	Events(ctx context.Context, images []string) (<-chan Event, <-chan error)
}

// ImageMatches reports whether a container's image belongs to this node.
// The match is a substring, so "ghcr.io/k4ryuu/cs2-egg-go" covers every tag
// of it and "sples1/k4ryuu-cs2:dev" covers exactly that one.
func ImageMatches(image string, images []string) bool {
	for _, want := range images {
		if want != "" && strings.Contains(image, want) {
			return true
		}
	}
	return false
}

type mobyDocker struct{ cli *client.Client }

// NewDocker connects to the engine through the usual environment.
func NewDocker() (Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return &mobyDocker{cli: cli}, nil
}

// BridgeSubnet returns the IPv4 subnet of a docker network (Wings uses
// pterodactyl_nw), or "" when docker or the network is not there.
func BridgeSubnet(ctx context.Context, name string) string {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return ""
	}
	defer cli.Close()
	res, err := cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		return ""
	}
	for _, c := range res.Network.IPAM.Config {
		if c.Subnet.IsValid() && c.Subnet.Addr().Is4() {
			return c.Subnet.String()
		}
	}
	return ""
}

func (d *mobyDocker) Running(ctx context.Context, images []string) ([]Container, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return nil, err
	}
	var out []Container
	for _, s := range res.Items {
		if len(s.Names) == 0 {
			continue
		}
		// a container started from a tag that has since been repulled shows
		// the image id here, not the name, so ask the container itself
		if !ImageMatches(s.Image, images) && !LooksLikeID(s.Image) {
			continue
		}
		c, err := d.Inspect(ctx, s.ID)
		if err != nil {
			continue // removed between list and inspect
		}
		if !ImageMatches(c.Image, images) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// LooksLikeID reports whether docker gave us an image id instead of a name.
// It does when the tag a container was started from has been repulled since:
// the list shows the digest and only the container itself still knows the
// name. Such a container is inspected rather than skipped.
func LooksLikeID(image string) bool {
	if rest, ok := strings.CutPrefix(image, "sha256:"); ok {
		image = rest
	}
	if len(image) < 12 {
		return false
	}
	for _, r := range image {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (d *mobyDocker) Inspect(ctx context.Context, id string) (Container, error) {
	res, err := d.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, err
	}
	return fromInspect(res.Container), nil
}

func fromInspect(in container.InspectResponse) Container {
	c := Container{ID: in.ID, Name: strings.TrimPrefix(in.Name, "/"), Image: in.Image}
	if in.Config != nil && in.Config.Image != "" {
		c.Image = in.Config.Image
	}
	for _, m := range in.Mounts {
		if m.Destination == "/home/container" {
			c.Volume = m.Source
		}
	}
	if in.State != nil {
		c.PID = in.State.Pid
	}
	if in.HostConfig != nil {
		c.Ports = hostPorts(in.HostConfig.PortBindings)
	}
	if c.Volume != "" {
		c.Owner = volumeOwner(c.Volume)
	}
	return c
}

// hostPorts is the sorted union of every published host port.
func hostPorts(pm network.PortMap) []uint16 {
	seen := map[uint16]bool{}
	for _, bindings := range pm {
		for _, b := range bindings {
			n, err := strconv.Atoi(b.HostPort)
			if err == nil && n > 0 && n < 65536 {
				seen[uint16(n)] = true
			}
		}
	}
	out := make([]uint16, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// VolumeOwner is the uid:gid of a volume root (zero when unreadable).
func VolumeOwner(path string) Owner { return volumeOwner(path) }

func volumeOwner(path string) Owner {
	st, err := os.Stat(path)
	if err != nil {
		return Owner{}
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return Owner{UID: int(sys.Uid), GID: int(sys.Gid)}
	}
	return Owner{}
}

func (d *mobyDocker) Events(ctx context.Context, images []string) (<-chan Event, <-chan error) {
	out := make(chan Event)
	errc := make(chan error, 1)
	f := client.Filters{}
	f.Add("type", "container")
	f.Add("event", "start", "die")
	res := d.cli.Events(ctx, client.EventsListOptions{Filters: f})
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-res.Err:
				if err == nil {
					err = io.EOF
				}
				errc <- err
				return
			case m := <-res.Messages:
				if m.Type != events.ContainerEventType {
					continue
				}
				image := m.Actor.Attributes["image"]
				if !ImageMatches(image, images) && !LooksLikeID(image) {
					continue
				}
				c := Container{ID: m.Actor.ID, Name: m.Actor.Attributes["name"], Image: image}
				kind, code := Die, -1
				if v, ok := m.Actor.Attributes["exitCode"]; ok {
					if n, err := strconv.Atoi(v); err == nil {
						code = n
					}
				}
				if m.Action == events.ActionStart {
					kind = Start
					if full, err := d.Inspect(ctx, m.Actor.ID); err == nil {
						c = full
					}
				}
				if !ImageMatches(c.Image, images) {
					continue // an id that turned out to be someone else's container
				}
				select {
				case out <- Event{Kind: kind, Container: c, ExitCode: code}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, errc
}
