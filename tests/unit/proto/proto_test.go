// SPDX-License-Identifier: GPL-3.0-or-later

package proto_test

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func roundTrip(t *testing.T, m proto.Message) proto.Message {
	t.Helper()
	var buf bytes.Buffer
	if err := proto.Encode(&buf, m); err != nil {
		t.Fatal(err)
	}
	line := buf.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("one message = one line: %q", line)
	}
	got, err := proto.Decode([]byte(strings.TrimSuffix(line, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestEveryMessageRoundTrips(t *testing.T) {
	msgs := []proto.Message{
		&proto.Hello{Version: "1.0.0", Modules: []string{"vpksync", "guard"}},
		&proto.Hello{Version: "1.0.0", BootID: "abc", Guard: &proto.GuardInfo{Enabled: true, Action: "block"}},
		&proto.VpkStatus{State: proto.StateQueued, QueuePos: 3},
		&proto.GuardBlock{IP: "1.2.3.4", Minutes: 60, Rule: "pps", Src: "host", UID: 12, Dry: true},
		&proto.GuardHit{IP: "1.2.3.4", Minutes: 30, Rule: "stray_no_connection"},
		&proto.GuardEvent{Kind: proto.EventJoin, IP: "1.2.3.4", UID: 7},
		&proto.ServerState{Map: "de_dust2", Players: 3, Up: true},
		&proto.ConsoleExec{Cmd: "say hi"},
		&proto.Crash{ExitCode: 134, Lines: []string{"a", "b"}, Map: "de_mirage"},
		&proto.LogLine{Kind: "server", Msg: "Map: de_dust2"},
		&proto.AddonQuery{Repo: "a/b", Prerelease: true},
		&proto.AddonRelease{Repo: "a/b", Tag: "v1", Assets: []proto.AddonAsset{{Name: "x.zip", URL: "https://x/x.zip", Path: "cache/x.zip"}}},
	}
	for _, m := range msgs {
		got := roundTrip(t, m)
		if got.Type() != m.Type() {
			t.Errorf("type changed: %s -> %s", m.Type(), got.Type())
		}
		var a, b bytes.Buffer
		proto.Encode(&a, m)
		proto.Encode(&b, got)
		if a.String() != b.String() {
			t.Errorf("not stable: %s vs %s", a.String(), b.String())
		}
	}
}

func TestUnknownTypeIsIgnorable(t *testing.T) {
	_, err := proto.Decode([]byte(`{"t":"future.thing","v":1,"d":{}}`))
	if err == nil || !proto.IsUnknown(err) {
		t.Fatalf("unknown type must be a recognisable error, got %v", err)
	}
	if _, err := proto.Decode([]byte(`garbage`)); err == nil || proto.IsUnknown(err) {
		t.Fatalf("garbage is a parse error, not unknown-type: %v", err)
	}
}

func TestReaderYieldsMessagesLineByLine(t *testing.T) {
	var buf bytes.Buffer
	proto.Encode(&buf, &proto.VpkStatus{State: proto.StateDone})
	buf.WriteString("{\"t\":\"future\",\"v\":1,\"d\":{}}\n")
	proto.Encode(&buf, &proto.GuardEvent{Kind: proto.EventLeave, IP: "9.9.9.9", UID: 1})
	r := proto.NewReader(bufio.NewReader(&buf))
	var kinds []proto.Type
	for {
		m, err := r.Next()
		if err != nil {
			break
		}
		kinds = append(kinds, m.Type())
	}
	if len(kinds) != 2 || kinds[0] != proto.TypeVpkStatus || kinds[1] != proto.TypeGuardEvent {
		t.Fatalf("reader must skip unknown types and stop at EOF: %v", kinds)
	}
}
