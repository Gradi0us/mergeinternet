// Package reorder puts packets that travelled over paths with different
// latencies back into send order before they reach the TUN device.
//
// TCP treats heavy reordering as loss and slows down, so for single-stream
// bonding the receiver waits up to MaxDelay for a missing sequence number
// before giving up on it.
package reorder

import (
	"sync"
	"time"

	"mergeinternet/internal/window"
)

// A jump this large means the sender restarted its sequence space; resync instead of buffering.
const maxJump = 1 << 20

type Stats struct {
	Delivered  uint64
	Duplicates uint64 // already buffered / already delivered
	Late       uint64 // arrived after we gave up on it
	Skipped    uint64 // sequence numbers never received
}

type entry struct {
	data []byte
	at   time.Time
}

type Buffer struct {
	mu         sync.Mutex
	maxDelay   time.Duration
	maxPending int
	out        func([]byte)

	next    uint64 // senders number packets from 1 in every session
	pending map[uint64]entry
	seen    window.Window // de-duplication when maxDelay == 0
	stats   Stats
}

// New returns a buffer that calls out, in sequence order, for each packet.
// maxDelay <= 0 disables reordering (packets are only de-duplicated).
func New(maxDelay time.Duration, maxPending int, out func([]byte)) *Buffer {
	if maxPending <= 0 {
		maxPending = 4096
	}
	return &Buffer{maxDelay: maxDelay, maxPending: maxPending, out: out, next: 1, pending: map[uint64]entry{}}
}

// Push takes ownership of data.
func (b *Buffer) Push(seq uint64, data []byte, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.maxDelay <= 0 {
		if !b.seen.Check(seq) {
			b.stats.Duplicates++
			return
		}
		b.deliver(data)
		return
	}

	if seq > b.next && seq-b.next > maxJump {
		b.next = seq
		clear(b.pending)
	}

	switch {
	case seq < b.next:
		b.stats.Late++
	case seq == b.next:
		b.deliver(data)
		b.next++
		b.drain()
	default:
		if _, dup := b.pending[seq]; dup {
			b.stats.Duplicates++
			return
		}
		b.pending[seq] = entry{data: data, at: now}
		if len(b.pending) > b.maxPending {
			b.skipToMin()
		}
	}
}

// Flush gives up on gaps that have been waiting longer than maxDelay.
// Call it periodically (every few milliseconds).
func (b *Buffer) Flush(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.pending) > 0 {
		oldest := now
		for _, e := range b.pending {
			if e.at.Before(oldest) {
				oldest = e.at
			}
		}
		if now.Sub(oldest) < b.maxDelay {
			return
		}
		b.skipToMin()
	}
}

func (b *Buffer) Reset() {
	b.mu.Lock()
	b.next = 1
	clear(b.pending)
	b.seen.Reset()
	b.mu.Unlock()
}

func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

func (b *Buffer) deliver(data []byte) {
	b.stats.Delivered++
	b.out(data)
}

func (b *Buffer) drain() {
	for {
		e, ok := b.pending[b.next]
		if !ok {
			return
		}
		delete(b.pending, b.next)
		b.deliver(e.data)
		b.next++
	}
}

func (b *Buffer) skipToMin() {
	min := ^uint64(0)
	for s := range b.pending {
		if s < min {
			min = s
		}
	}
	b.stats.Skipped += min - b.next
	b.next = min
	b.drain()
}
