// SPDX-License-Identifier: GPL-3.0-or-later

package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
)

// maxEmbeds Discord accepts in one message.
const maxEmbeds = 10

// Embed is one Discord embed.
type Embed struct {
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Color       int     `json:"color"`
	Timestamp   string  `json:"timestamp"`
	Fields      []Field `json:"fields,omitempty"`
	Author      *author `json:"author,omitempty"`
	Footer      *footer `json:"footer,omitempty"`
}

// author is the line above the title. The server a message is about goes
// there: a panel id identifies a row in a table, a name identifies a server
// to the person reading the channel.
type author struct {
	Name string `json:"name"`
}

// Field is one labelled fact. Three inline fields sit on a row, which is
// what turns a sentence into something you can read at a glance.
type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type footer struct {
	Text string `json:"text"`
}

func (e *Embed) add(name, value string) {
	if value == "" || value == "<nil>" {
		return
	}
	e.Fields = append(e.Fields, Field{Name: name, Value: value, Inline: true})
}

// code wraps a value so Discord renders it in a monospace span.
func code(s string) string {
	if s == "" || s == "<nil>" {
		return ""
	}
	return "`" + s + "`"
}

const (
	red    = 0xE74C3C
	orange = 0xE67E22
	yellow = 0xF1C40F
	green  = 0x2ECC71
	blue   = 0x3498DB
	purple = 0x9B59B6
	gray   = 0x95A5A6
)

// Render turns one notice into a Discord embed. The facts go in inline
// fields rather than a sentence, and the server and node sit in the footer
// where a 36 character panel id has room to breathe.
func Render(n core.Notice, node string) Embed {
	f := func(k string) string { return fmt.Sprint(n.Fields[k]) }
	e := Embed{Timestamp: n.Time.UTC().Format(time.RFC3339), Color: gray}
	switch n.Kind {
	case core.NoticeCrash:
		e.Title, e.Color = "Server crashed", red
		e.add("Exit code", code(f("exit_code")))
		e.add("Map", orDash(f("map")))
		e.add("Players", orDash(f("players")))
		e.Description = "Console tail and dumps: " + code("cs2node crash "+ui.ServerID(n.Container))
	case core.NoticeCS2Update:
		e.Title, e.Color = "CS2 updated", blue
		e.add("Build", code(f("from"))+" → "+code(f("to")))
		e.Description = "Pushing to every server on this node."
	case core.NoticePushFailed:
		e.Title, e.Color = "VPK push failed", red
		e.Description = f("err")
	case core.NoticeRestartDeferred:
		e.Title, e.Color = "Restart deferred", yellow
		e.add("Until", f("until"))
		e.add("Waiting for", f("reason"))
		e.Description = "The server runs the old build until then."
	case core.NoticeGuardBlock:
		e.Title, e.Color = "Address blocked", orange
		e.add("Address", code(f("ip")))
		e.add("Blocked for", forMinutes(n.Fields["minutes"]))
		e.add("Rule", code(f("rule")))
		if r := count(n.Fields["repeat"]); r > 1 {
			e.add("Offence", "#"+strconv.Itoa(r))
		}
		if src := f("src"); src == "egg" {
			e.add("Caught by", "the server console")
		} else if src == "host" {
			e.add("Caught by", "traffic shape")
		}
		e.Description = detail(f("detail"))
	case core.NoticeNodeUpdate:
		e.Title, e.Color = "cs2node updated", purple
		e.add("Version", code(f("from"))+" → "+code(f("to")))
		e.Description = "The daemon is restarting into the new build."
	case core.NoticeBackupDone:
		e.Title, e.Color = "Backup done", green
		e.add("Size", size(n.Fields["bytes"]))
		e.add("Files", orDash(f("files")))
		e.Description = code(f("path"))
	case core.NoticeBackupFailed:
		e.Title, e.Color = "Backup failed", red
		e.Description = f("err")
	case core.NoticeCleanupDone:
		e.Title, e.Color = "Cleanup done", blue
		e.add("Freed", size(n.Fields["bytes"]))
		e.add("Files", orDash(f("files")))
	case core.NoticeServerStart:
		e.Title, e.Color = "Server up", green
		e.add("Ports", ports(n.Fields["ports"]))
		e.add("Image", code(f("image")))
	case core.NoticeServerStop:
		e.Title, e.Color = "Server down", gray
		switch f("reason") {
		case core.StopCrash:
			// the crash notice carries the detail and the bundle pointer,
			// so this one stays a stop, only a red one
			e.Color = red
			e.add("Why", "it exited on a crash")
		case core.StopRestart:
			e.add("Why", "restarting for a CS2 update")
		case core.StopStopped:
			e.add("Why", "stopped cleanly")
		case core.StopKilled:
			e.Color = yellow
			e.add("Why", "killed, out of memory or a forced stop")
		default:
			e.add("Why", "docker did not say")
		}
		// CS2 exits on a segfault every single clean stop, so the code is
		// only worth showing when it is part of the answer
		if code := count(n.Fields["exit_code"]); code > 0 && f("reason") != core.StopStopped {
			e.add("Exit code", code2str(code))
		}
	default:
		e.Title = n.Kind
	}
	if n.Server != "" {
		e.Author = &author{Name: n.Server}
	}
	// the short id is what every cs2node command takes, so it is the one
	// worth carrying; the full panel id is a click away in the panel
	e.Footer = &footer{Text: strings.TrimSpace(strings.Join(nonEmpty(node, ui.ServerID(n.Container)), "  ·  "))}
	if e.Footer.Text == "" {
		e.Footer = nil
	}
	return e
}

func nonEmpty(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// detail drops a value that says nothing, so an empty measurement does not
// leave a stray line under the fields.
func detail(s string) string {
	if s == "" || s == "<nil>" {
		return ""
	}
	return s
}

// ports renders the published game ports, whatever numeric shape they
// arrived in after a round trip through JSON.
func ports(v any) string {
	var out []string
	switch x := v.(type) {
	case []uint16:
		for _, p := range x {
			out = append(out, strconv.Itoa(int(p)))
		}
	case []any:
		for _, p := range x {
			if n := count(p); n > 0 {
				out = append(out, strconv.Itoa(n))
			}
		}
	}
	return strings.Join(out, ", ")
}

// code2str names the signal behind an exit code, because 139 means
// nothing to most people and "segfault" means everything.
func code2str(code int) string {
	if code > 128 {
		if name, ok := signals[code-128]; ok {
			return fmt.Sprintf("%d (%s)", code, name)
		}
	}
	return strconv.Itoa(code)
}

var signals = map[int]string{2: "interrupt", 6: "abort", 9: "killed", 11: "segfault", 15: "terminated"}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func count(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// forMinutes reads the way an operator thinks about a ban: 1440 is a day,
// not a number to divide in your head.
func forMinutes(v any) string {
	m := count(v)
	switch {
	case m <= 0:
		return "-"
	case m < 60:
		return strconv.Itoa(m) + " min"
	case m%1440 == 0 && m > 1440:
		return plural(m/1440, "day")
	case m%60 == 0:
		return plural(m/60, "hour")
	}
	return strconv.Itoa(m/60) + "h " + strconv.Itoa(m%60) + "m"
}

func orDash(s string) string { return ui.Or(s, "-") }

func size(v any) string {
	switch x := v.(type) {
	case int64:
		return ui.Size(x)
	case int:
		return ui.Size(int64(x))
	case float64:
		return ui.Size(int64(x))
	}
	return "?"
}

// Post sends up to maxEmbeds notices as one message, under the name and
// picture the config asks for. A 429 is retried once after Discord's
// retry_after.
func Post(ctx context.Context, client *http.Client, cfg Config, notices []core.Notice) error {
	embeds := make([]Embed, 0, len(notices))
	for _, n := range notices {
		embeds = append(embeds, Render(n, cfg.Name))
	}
	payload := map[string]any{"embeds": embeds}
	if cfg.Username != "" {
		payload["username"] = cfg.Username
	}
	if cfg.Avatar != "" {
		payload["avatar_url"] = cfg.Avatar
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Webhook, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "cs2node")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests && attempt == 0:
			var r struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.Unmarshal(reply, &r)
			wait := time.Duration(r.RetryAfter*float64(time.Second)) + 100*time.Millisecond
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return fmt.Errorf("discord %d: %s", resp.StatusCode, strings.TrimSpace(string(reply)))
		}
	}
	return errors.New("discord kept rate limiting")
}

// Lookup GETs the webhook, which returns its name without posting. Used by
// doctor.
func Lookup(ctx context.Context, client *http.Client, webhook string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webhook, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cs2node")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discord answered %d (deleted webhook, or a wrong URL)", resp.StatusCode)
	}
	var w struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&w); err != nil {
		return "", err
	}
	return w.Name, nil
}
