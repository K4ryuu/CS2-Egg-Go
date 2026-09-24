// SPDX-License-Identifier: GPL-3.0-or-later

package boot

import (
	"context"
	"sync/atomic"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// NodeLogQueue is how many lines can wait for the node before Write starts
// dropping them instead of blocking the caller.
const NodeLogQueue = 2048

// NodeLogSink forwards console lines to the node's console-log module for
// host-side persistence. Write never blocks: it runs on srv.Line, the same
// goroutine that reads the game process's stdout pipe, and a slow or gone
// node must not stall that pipe or the server behind it.
type NodeLogSink struct {
	ch      chan proto.LogLine
	dropped atomic.Int64
}

// NewNodeLogSink starts the drain goroutine that hands lines to send, until
// ctx ends.
func NewNodeLogSink(ctx context.Context, send func(proto.Message)) *NodeLogSink {
	s := &NodeLogSink{ch: make(chan proto.LogLine, NodeLogQueue)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case l := <-s.ch:
				send(&l)
			}
		}
	}()
	return s
}

var _ logx.Sink = (*NodeLogSink)(nil)

func (s *NodeLogSink) Write(kind, msg string) {
	select {
	case s.ch <- proto.LogLine{Kind: kind, Msg: msg}:
	default:
		s.dropped.Add(1)
	}
}

// Dropped is how many lines never reached the node because the queue was
// full. Checked from a different goroutine than Write, never from it.
func (s *NodeLogSink) Dropped() int64 { return s.dropped.Load() }
