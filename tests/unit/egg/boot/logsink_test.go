// SPDX-License-Identifier: GPL-3.0-or-later

package boot_test

import (
	"context"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/boot"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Write must never block the caller, even when nothing drains the queue:
// it is called from the same goroutine that reads the game process's
// stdout pipe, and a stalled node must not stall that.
func TestNodeLogSinkNeverBlocksAndDropsWhenFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	block := make(chan struct{}) // never closed: send() always blocks here
	sink := boot.NewNodeLogSink(ctx, func(proto.Message) { <-block })

	done := make(chan struct{})
	go func() {
		for i := 0; i < boot.NodeLogQueue+50; i++ {
			sink.Write("info", "line")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked: a stalled node must not stall the caller")
	}
	if sink.Dropped() == 0 {
		t.Fatal("queue overflowed, some lines must have been dropped")
	}
}

// Once the drain goroutine can keep up, every line reaches send in order.
func TestNodeLogSinkDeliversInOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []string
	done := make(chan struct{})
	send := func(m proto.Message) {
		l := m.(*proto.LogLine)
		got = append(got, l.Msg)
		if len(got) == 3 {
			close(done)
		}
	}
	sink := boot.NewNodeLogSink(ctx, send)
	sink.Write("info", "one")
	sink.Write("info", "two")
	sink.Write("server", "three")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lines never arrived")
	}
	want := []string{"one", "two", "three"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if sink.Dropped() != 0 {
		t.Fatalf("nothing should have dropped: %d", sink.Dropped())
	}
}
