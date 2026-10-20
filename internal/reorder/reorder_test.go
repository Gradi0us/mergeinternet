package reorder

import (
	"slices"
	"testing"
	"time"
)

type sink struct{ got []uint64 }

func (s *sink) out(b []byte) { s.got = append(s.got, uint64(b[0])) }

func pkt(seq uint64) []byte { return []byte{byte(seq)} }

func TestReordersOutOfOrderArrival(t *testing.T) {
	var s sink
	b := New(50*time.Millisecond, 0, s.out)
	now := time.Now()
	for _, seq := range []uint64{1, 3, 4, 2, 6, 5} {
		b.Push(seq, pkt(seq), now)
	}
	if want := []uint64{1, 2, 3, 4, 5, 6}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
}

func TestDropsDuplicatesAndLate(t *testing.T) {
	var s sink
	b := New(50*time.Millisecond, 0, s.out)
	now := time.Now()
	for _, seq := range []uint64{1, 1, 3, 3, 2, 2} {
		b.Push(seq, pkt(seq), now)
	}
	if want := []uint64{1, 2, 3}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
	// The repeated 3 was still buffered (duplicate); the repeated 1 and 2 were already delivered (late).
	if st := b.Stats(); st.Duplicates != 1 || st.Late != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestFlushSkipsLostPacketAfterDelay(t *testing.T) {
	var s sink
	b := New(50*time.Millisecond, 0, s.out)
	t0 := time.Now()
	b.Push(1, pkt(1), t0)
	b.Push(3, pkt(3), t0) // 2 is lost
	b.Push(4, pkt(4), t0)
	b.Flush(t0.Add(10 * time.Millisecond))
	if want := []uint64{1}; !slices.Equal(s.got, want) {
		t.Fatalf("flushed too early: %v", s.got)
	}
	b.Flush(t0.Add(60 * time.Millisecond))
	if want := []uint64{1, 3, 4}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
	if st := b.Stats(); st.Skipped != 1 {
		t.Fatalf("skipped = %d", st.Skipped)
	}
	b.Push(2, pkt(2), t0.Add(70*time.Millisecond)) // finally arrives: too late
	if len(s.got) != 3 {
		t.Fatal("late packet delivered out of order")
	}
}

func TestMaxPendingForcesSkip(t *testing.T) {
	var s sink
	b := New(time.Hour, 2, s.out)
	now := time.Now()
	b.Push(1, pkt(1), now)
	for _, seq := range []uint64{3, 4, 5} { // 2 never arrives; third pending exceeds limit
		b.Push(seq, pkt(seq), now)
	}
	if want := []uint64{1, 3, 4, 5}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
}

func TestPassThroughModeOnlyDeduplicates(t *testing.T) {
	var s sink
	b := New(0, 0, s.out)
	now := time.Now()
	for _, seq := range []uint64{2, 1, 2, 3} {
		b.Push(seq, pkt(seq), now)
	}
	if want := []uint64{2, 1, 3}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
}

func TestFirstPacketsOfSessionMayArriveReordered(t *testing.T) {
	var s sink
	b := New(50*time.Millisecond, 0, s.out)
	now := time.Now()
	for _, seq := range []uint64{3, 1, 2} { // 3 took a faster path
		b.Push(seq, pkt(seq), now)
	}
	if want := []uint64{1, 2, 3}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
}

func TestResetAcceptsRestartedSequence(t *testing.T) {
	var s sink
	b := New(50*time.Millisecond, 0, s.out)
	now := time.Now()
	b.Push(1, pkt(1), now)
	b.Push(2, pkt(2), now)
	b.Reset()
	b.Push(1, pkt(1), now)
	if want := []uint64{1, 2, 1}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
}

func TestHugeJumpResyncs(t *testing.T) {
	var s sink
	b := New(time.Hour, 0, s.out)
	now := time.Now()
	b.Push(1, pkt(1), now)
	b.Push(5+maxJump, pkt(9), now)
	if want := []uint64{1, 9}; !slices.Equal(s.got, want) {
		t.Fatalf("got %v, want %v", s.got, want)
	}
}
