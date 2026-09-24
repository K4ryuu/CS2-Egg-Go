// SPDX-License-Identifier: GPL-3.0-or-later

package vpksync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
)

// Window is a daily local-time span; it may wrap midnight.
type Window struct{ From, To int } // minutes since midnight

// ParseWindow reads "HH:MM-HH:MM".
func ParseWindow(s string) (Window, error) {
	var w Window
	var fh, fm, th, tm int
	if n, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d-%d:%d", &fh, &fm, &th, &tm); err != nil || n != 4 || fh > 23 || th > 23 || fm > 59 || tm > 59 || fh < 0 || th < 0 || fm < 0 || tm < 0 {
		return w, fmt.Errorf("restart_window %q must be HH:MM-HH:MM", s)
	}
	w.From, w.To = fh*60+fm, th*60+tm
	if w.From == w.To {
		return w, fmt.Errorf("restart_window %q is empty", s)
	}
	return w, nil
}

// Open reports whether t (local) falls inside the window.
func (w Window) Open(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if w.From < w.To {
		return m >= w.From && m < w.To
	}
	return m >= w.From || m < w.To // wraps midnight
}

func (w Window) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.From/60, w.From%60, w.To/60, w.To%60)
}

// Decision for one server with a restart owed.
type Decision int

const (
	Wait      Decision = iota // players on, not yet time
	Restart                   // now
	Countdown                 // players on but it is time: warn, then restart
)

// Decide applies the policy. known says the egg reported a state at all (an
// old image never does, it restarts right away as before).
func Decide(cfg Config, since time.Time, players int, known bool, now time.Time) Decision {
	if cfg.RestartPolicy == "" || cfg.RestartPolicy == Immediate || !known || players == 0 {
		return Restart
	}
	if cfg.MaxDelayHours > 0 && now.Sub(since) >= time.Duration(cfg.MaxDelayHours)*time.Hour {
		return Countdown
	}
	if cfg.RestartPolicy == InWindow {
		if w, err := ParseWindow(cfg.RestartWindow); err == nil && w.Open(now) {
			return Countdown
		}
	}
	return Wait
}

// owed is one server waiting for its restart.
type owed struct {
	since     time.Time
	restartAt time.Time // set once the countdown started
	lastWarn  int       // minutes announced last, to say each step once
}

// reason is the human line for a deferral.
func (m *Module) reason() string {
	switch m.cfg.RestartPolicy {
	case InWindow:
		return "waiting for an empty server or the window " + m.cfg.RestartWindow
	default:
		return "waiting for an empty server"
	}
}

// owe registers a restart for the container after a successful push and
// applies the policy right away.
func (m *Module) owe(ctx context.Context, c *core.Core, name string) {
	now := time.Now()
	m.mu.Lock()
	if _, already := m.owed[name]; !already {
		m.owed[name] = &owed{since: now}
	}
	m.mu.Unlock()
	if !m.settle(ctx, c, name, now) {
		fields := map[string]any{"reason": m.reason(), "until": "no deadline"}
		if m.cfg.MaxDelayHours > 0 {
			fields["until"] = now.Add(time.Duration(m.cfg.MaxDelayHours) * time.Hour).Format("2006-01-02 15:04")
		}
		m.Log.Info("restart deferred", "container", name, "reason", m.reason())
		c.Notify(core.NoticeRestartDeferred, name, fields)
	}
}

// settle runs the policy for one owed server; true when it restarted.
func (m *Module) settle(ctx context.Context, c *core.Core, name string, now time.Time) bool {
	m.mu.Lock()
	o, ok := m.owed[name]
	m.mu.Unlock()
	if !ok {
		return false
	}
	st, known := c.State(name)
	players := st.Players
	if !o.restartAt.IsZero() && known && players > 0 {
		// countdown running: say the remaining minutes once per step
		left := o.restartAt.Sub(now)
		switch mins := int(left.Minutes() + 0.5); {
		case left <= 30*time.Second:
		case mins >= 1 && mins != o.lastWarn && (mins <= 2 || mins%5 == 0):
			o.lastWarn = mins
			c.Exec(name, fmt.Sprintf("say Server restarts in %d minute(s) for a CS2 update", mins))
			return false
		default:
			return false
		}
	} else {
		switch Decide(m.cfg, o.since, players, known, now) {
		case Wait:
			return false
		case Countdown:
			if m.cfg.WarnMinutes > 0 && o.restartAt.IsZero() {
				o.restartAt = now.Add(time.Duration(m.cfg.WarnMinutes) * time.Minute)
				o.lastWarn = m.cfg.WarnMinutes
				c.Exec(name, fmt.Sprintf("say Server restarts in %d minute(s) for a CS2 update", m.cfg.WarnMinutes))
				return false
			}
		}
	}
	m.mu.Lock()
	delete(m.owed, name)
	m.mu.Unlock()
	if known && players > 0 {
		c.Exec(name, "say Restarting now for a CS2 update")
	}
	m.restart(ctx, c, name)
	return true
}

// restartLoop re-runs the policy for every owed server.
func (m *Module) restartLoop(ctx context.Context, c *core.Core) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.Lock()
			names := make([]string, 0, len(m.owed))
			for n := range m.owed {
				names = append(names, n)
			}
			m.mu.Unlock()
			for _, n := range names {
				m.settle(ctx, c, n, now)
			}
		}
	}
}

// forget drops an owed restart (the container went away on its own).
func (m *Module) forget(name string) {
	m.mu.Lock()
	delete(m.owed, name)
	m.mu.Unlock()
}
