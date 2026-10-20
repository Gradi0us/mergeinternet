// Package window implements a sliding anti-replay / de-duplication window
// (ring-of-blocks variant from RFC 6479, as used by WireGuard).
package window

import "sync"

const (
	blockBitLog = 6
	blockBits   = 1 << blockBitLog // 64
	ringBlocks  = 128
	blockMask   = ringBlocks - 1
	bitMask     = blockBits - 1
	// Size is how far behind the highest seen value a value may arrive and still be accepted.
	Size = (ringBlocks - 1) * blockBits // 8128
)

// Window is safe for concurrent use. The zero value is ready to use.
type Window struct {
	mu   sync.Mutex
	last uint64
	ring [ringBlocks]uint64
}

func (w *Window) Reset() {
	w.mu.Lock()
	w.last = 0
	w.ring = [ringBlocks]uint64{}
	w.mu.Unlock()
}

// Check reports whether v has not been seen before (and is not too old), and records it.
func (w *Window) Check(v uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	indexBlock := v >> blockBitLog
	if v > w.last {
		current := w.last >> blockBitLog
		diff := indexBlock - current
		if diff > ringBlocks {
			diff = ringBlocks
		}
		for i := current + 1; i <= current+diff; i++ {
			w.ring[i&blockMask] = 0
		}
		w.last = v
	} else if w.last-v > Size {
		return false
	}
	indexBlock &= blockMask
	bit := uint64(1) << (v & bitMask)
	old := w.ring[indexBlock]
	w.ring[indexBlock] = old | bit
	return old&bit == 0
}
