// SPDX-License-Identifier: GPL-3.0-or-later

package alerts_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/alerts"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
)

type embed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
	Fields      []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Inline bool   `json:"inline"`
	} `json:"fields"`
	Footer struct {
		Text string `json:"text"`
	} `json:"footer"`
}

// field returns the value of a named field, or "" when the embed has none.
func (e embed) field(name string) string {
	for _, f := range e.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

type post struct {
	Username string  `json:"username"`
	Avatar   string  `json:"avatar_url"`
	Embeds   []embed `json:"embeds"`
}

func hook(t *testing.T) (*httptest.Server, func() []post) {
	var mu sync.Mutex
	var got []post
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"name":"cs2-alerts"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var p post
		if err := json.Unmarshal(body, &p); err != nil {
			t.Errorf("bad payload: %v", err)
		}
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []post {
		mu.Lock()
		defer mu.Unlock()
		return append([]post(nil), got...)
	}
}

func newModule(t *testing.T, srv *httptest.Server, events ...string) (*alerts.Module, *core.Core) {
	cfg := nodeconfig.Default()
	cfg.SetSection("alerts", alerts.Config{Enabled: true, Webhook: srv.URL, Events: events, Name: "game2"})
	c := core.New(nil, cfg, []string{"alerts"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m := alerts.New(slog.New(slog.NewTextHandler(io.Discard, nil)), srv.Client())
	m.FlushDelay = 20 * time.Millisecond
	return m, c
}

func waitPosts(t *testing.T, get func() []post, n int) []post {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := get(); len(p) >= n {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d post(s), got %d", n, len(get()))
	return nil
}

func TestNoticesArriveTogetherAsOneMessage(t *testing.T) {
	srv, got := hook(t)
	m, c := newModule(t, srv, "all")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx, c)
	time.Sleep(20 * time.Millisecond) // let Run register its listener
	c.Notify(core.NoticeCrash, "srv-1", map[string]any{"exit_code": 134, "map": "de_dust2", "players": 5})
	c.Notify(core.NoticeGuardBlock, "srv-1", map[string]any{"ip": "203.0.113.7", "minutes": 30, "rule": "connect_flood", "why": "6 hits in 10s"})
	posts := waitPosts(t, got, 1)
	if len(posts) != 1 || len(posts[0].Embeds) != 2 {
		t.Fatalf("want one post with two embeds, got %+v", posts)
	}
	crash := posts[0].Embeds[0]
	if crash.Title != "Server crashed" || crash.field("Map") != "de_dust2" || crash.field("Exit code") != "`134`" {
		t.Fatalf("crash embed: %+v", crash)
	}
	// the node and the server both belong in the footer: a 36 character
	// panel id wrapped mid-token when it sat in the sentence
	if crash.Footer.Text != "game2  ·  srv-1" {
		t.Fatalf("crash footer: %q", crash.Footer.Text)
	}
	block := posts[0].Embeds[1]
	if block.field("Address") != "`203.0.113.7`" || block.field("Blocked for") != "30 min" || block.field("Rule") != "`connect_flood`" {
		t.Fatalf("block embed: %+v", block)
	}
}

// The webhook is named by whoever made it, which is rarely what the posts
// should say they are.
func TestPostsCarryTheConfiguredIdentity(t *testing.T) {
	srv, got := hook(t)
	defer srv.Close()
	cfg := alerts.Config{Webhook: srv.URL, Name: "game2", Username: "cs2node", Avatar: "https://example.invalid/a.png"}
	if err := alerts.Post(context.Background(), srv.Client(), cfg, []core.Notice{{Kind: core.NoticeServerStop, Container: "x", Time: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	posts := waitPosts(t, got, 1)
	if posts[0].Username != "cfgname" && posts[0].Username != "cs2node" {
		t.Fatalf("username not sent: %+v", posts[0])
	}
	if posts[0].Avatar != "https://example.invalid/a.png" {
		t.Fatalf("avatar not sent: %+v", posts[0])
	}
}

func TestOnlyWantedEventsArePosted(t *testing.T) {
	srv, got := hook(t)
	m, c := newModule(t, srv, core.NoticeCrash)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx, c)
	time.Sleep(20 * time.Millisecond)
	c.Notify(core.NoticeServerStart, "srv-1", nil)
	c.Notify(core.NoticeCrash, "srv-1", map[string]any{"exit_code": 1})
	posts := waitPosts(t, got, 1)
	if len(posts[0].Embeds) != 1 || posts[0].Embeds[0].Title != "Server crashed" {
		t.Fatalf("filter: %+v", posts)
	}
}

func TestRateLimitIsRetriedOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"retry_after":0.01}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	err := alerts.Post(context.Background(), srv.Client(), alerts.Config{Webhook: srv.URL, Name: "n"}, []core.Notice{{Kind: core.NoticeServerStop, Container: "x", Time: time.Now()}})
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestConfigCheck(t *testing.T) {
	if err := (alerts.Config{Webhook: "http://x"}).Check(); err == nil {
		t.Fatal("plain http accepted")
	}
	if err := (alerts.Config{Webhook: "https://x", Events: []string{"nope"}}).Check(); err == nil {
		t.Fatal("unknown event accepted")
	}
	if err := (alerts.Config{Webhook: "https://x", Events: []string{"crash", "all"}}).Check(); err != nil {
		t.Fatal(err)
	}
}

// Every kind the wizard offers has to render as a real embed. A kind with
// no case falls back to its own name as the title, which is how the cleanup
// notice shipped unreadable the first time.
func TestEveryNoticeKindRenders(t *testing.T) {
	fields := map[string]any{
		"exit_code": 134, "map": "de_dust2", "players": 12, "from": "1", "to": "2",
		"err": "boom", "until": "05:00", "reason": "empty", "ip": "1.2.3.4", "minutes": 60,
		"rule": "pps", "why": "flood", "path": "/srv/x.tar.gz", "bytes": int64(1 << 30), "files": 9,
	}
	for _, k := range core.NoticeKinds {
		e := alerts.Render(core.Notice{Kind: k.Kind, Container: "59e9667c-9616-4ba9-be7e-3deca6c8806c",
			Server: "Retake #1", Time: time.Now(), Fields: fields}, "node1")
		if e.Title == "" || e.Title == k.Kind {
			t.Errorf("%s: no title of its own (%q)", k.Kind, e.Title)
		}
		// an embed must say what it is about: either in its own body, or
		// in the footer that names the node and the server
		if e.Description == "" && len(e.Fields) == 0 && (e.Footer == nil || e.Footer.Text == "") {
			t.Errorf("%s: nothing but a title", k.Kind)
		}
		if strings.Contains(e.Description, "<nil>") {
			t.Errorf("%s: a missing field leaked into the text: %s", k.Kind, e.Description)
		}
		// every message about a server says which one, by the name people
		// know it by and by the id the commands take
		if e.Author == nil || e.Author.Name != "Retake #1" {
			t.Errorf("%s: no server name on the message: %+v", k.Kind, e.Author)
		}
		if e.Footer == nil || !strings.Contains(e.Footer.Text, "59e9667c") {
			t.Errorf("%s: no short id in the footer: %+v", k.Kind, e.Footer)
		}
		if e.Footer != nil && strings.Contains(e.Footer.Text, "3deca6c8806c") {
			t.Errorf("%s: the full panel id is still in the footer: %q", k.Kind, e.Footer.Text)
		}
		for _, fl := range e.Fields {
			if fl.Value == "" || strings.Contains(fl.Value, "<nil>") {
				t.Errorf("%s: field %q renders as %q", k.Kind, fl.Name, fl.Value)
			}
		}
	}
}

// A ban is a duration an operator thinks in, not a minute count to divide
// in your head.
func TestBanDurationReadsLikeEnglish(t *testing.T) {
	want := map[int]string{15: "15 min", 60: "1 hour", 90: "1h 30m", 120: "2 hours", 1440: "24 hours", 2880: "2 days"}
	for min, text := range want {
		e := alerts.Render(core.Notice{Kind: core.NoticeGuardBlock, Time: time.Now(),
			Fields: map[string]any{"ip": "1.2.3.4", "minutes": min, "rule": "x"}}, "n")
		got := ""
		for _, f := range e.Fields {
			if f.Name == "Blocked for" {
				got = f.Value
			}
		}
		if got != text {
			t.Errorf("%d min: got %q want %q", min, got, text)
		}
	}
}

// CS2 exits on a segfault on every clean stop, so the exit code cannot be
// the thing that decides. The egg knows, because it knows whether it asked
// the server to quit, and it says nothing when it did.
func TestACleanStopIsNotReportedAsACrash(t *testing.T) {
	e := alerts.Render(core.Notice{Kind: core.NoticeServerStop, Container: "59e9667c-1111-2222-3333-444444444444",
		Server: "Retake #1", Time: time.Now(),
		Fields: map[string]any{"reason": core.StopStopped, "exit_code": 139}}, "game2")
	for _, f := range e.Fields {
		if strings.Contains(strings.ToLower(f.Value), "crash") {
			t.Fatalf("a clean stop must not read as a crash: %q", f.Value)
		}
		if f.Name == "Exit code" {
			t.Fatalf("a meaningless exit code must not be shown: %q", f.Value)
		}
	}
	// the message says which server, by name, and by the id commands take
	if e.Author == nil || e.Author.Name != "Retake #1" {
		t.Fatalf("the server name belongs on the message: %+v", e.Author)
	}
	if e.Footer == nil || !strings.Contains(e.Footer.Text, "59e9667c") || strings.Contains(e.Footer.Text, "4444") {
		t.Fatalf("the footer carries the short id: %+v", e.Footer)
	}
}
