package engine

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"mergeinternet/internal/proto"
)

const (
	// speedFillSize keeps the sealed datagram (plus Overhead) under the ~1360 MTU used elsewhere.
	speedFillSize = 1200
	// speedMaxDur bounds any flood, whatever a request asks for.
	speedMaxDur      = 5 * time.Second
	speedDefaultDur  = 3 * time.Second
	speedDefaultRate = 1000 // Mbit/s
	speedMaxRate     = 2000
	// speedKeep is how many finished/in-flight tests the server remembers.
	speedKeep = 16
)

// speedTest is the receive-side tally of one test. Bytes are counted from the
// second datagram on, so bytes/(last-first) is a true rate between two arrivals.
type speedTest struct {
	bytes       uint64
	first, last time.Time
	res         chan speedRes // client upload test: the server's answer
}

type speedRes struct {
	bytes uint64
	span  time.Duration
}

type flood struct {
	testID uint32
	cancel context.CancelFunc
}

// speedState holds all speed-test bookkeeping of one engine.
type speedState struct {
	mu     sync.Mutex
	tests  map[uint32]*speedTest
	order  []uint32         // insertion order, for bounded memory on the server
	floods map[uint8]*flood // server: the latest flood per link
}

// begin registers a client-side test so its fills / result are accepted.
func (s *speedState) begin(id uint32, withRes bool) *speedTest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tests == nil {
		s.tests = map[uint32]*speedTest{}
	}
	t := &speedTest{}
	if withRes {
		t.res = make(chan speedRes, 1)
	}
	s.tests[id] = t
	return t
}

func (s *speedState) end(id uint32) {
	s.mu.Lock()
	delete(s.tests, id)
	s.mu.Unlock()
}

// note counts one received fill datagram. The server creates unknown tests on
// demand (evicting the oldest); the client only counts tests it started.
func (s *speedState) note(id uint32, n int, now time.Time, create bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tests[id]
	if t == nil {
		if !create {
			return
		}
		if s.tests == nil {
			s.tests = map[uint32]*speedTest{}
		}
		if len(s.order) >= speedKeep {
			delete(s.tests, s.order[0])
			s.order = s.order[1:]
		}
		t = &speedTest{}
		s.tests[id] = t
		s.order = append(s.order, id)
	}
	if t.first.IsZero() {
		t.first = now // first datagram only starts the clock
	} else {
		t.bytes += uint64(n)
	}
	t.last = now
}

func (s *speedState) snapshot(id uint32) (bytes uint64, first, last time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tests[id]; t != nil {
		return t.bytes, t.first, t.last
	}
	return 0, time.Time{}, time.Time{}
}

// handleSpeed processes SPEED_REQ / FILL / RES. It never blocks: floods run in
// their own goroutine.
func (e *Engine) handleSpeed(l *Link, t proto.Type, pt []byte, wireLen int, now time.Time) {
	switch t {
	case proto.TypeSpeedFill:
		if id, err := proto.ParseSpeedFill(pt); err == nil {
			e.speed.note(id, wireLen, now, e.cfg.Role == Server)
		}
	case proto.TypeSpeedReq:
		if e.cfg.Role != Server {
			return
		}
		id, op, dur, capMbps, err := proto.ParseSpeedReq(pt)
		if err != nil {
			return
		}
		switch op {
		case proto.SpeedDown:
			e.startFlood(l, id, dur, capMbps)
		case proto.SpeedUpResult:
			bytes, first, last := e.speed.snapshot(id)
			var span time.Duration
			if !first.IsZero() {
				span = last.Sub(first)
			}
			e.sealAndSend(l, proto.TypeSpeedRes, proto.PutSpeedRes(id, bytes, span))
		}
	case proto.TypeSpeedRes:
		if e.cfg.Role != Client {
			return
		}
		id, bytes, span, err := proto.ParseSpeedRes(pt)
		if err != nil {
			return
		}
		e.speed.mu.Lock()
		tst := e.speed.tests[id]
		e.speed.mu.Unlock()
		if tst != nil && tst.res != nil {
			select {
			case tst.res <- speedRes{bytes, span}:
			default: // an earlier answer is already waiting
			}
		}
	}
}

func clampDur(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return speedDefaultDur
	case d > speedMaxDur:
		return speedMaxDur
	}
	return d
}

func clampRate(mbps int) int {
	switch {
	case mbps <= 0:
		return speedDefaultRate
	case mbps > speedMaxRate:
		return speedMaxRate
	}
	return mbps
}

// startFlood (server) floods link l for the requested time. One flood per link:
// the same testID again is ignored, a new testID replaces the running one.
func (e *Engine) startFlood(l *Link, testID uint32, dur time.Duration, capMbps uint16) {
	parent := context.Background()
	if p := e.runCtx.Load(); p != nil {
		parent = *p
	}
	ctx, cancel := context.WithCancel(parent)
	e.speed.mu.Lock()
	if e.speed.floods == nil {
		e.speed.floods = map[uint8]*flood{}
	}
	if cur := e.speed.floods[l.ID]; cur != nil {
		if cur.testID == testID {
			e.speed.mu.Unlock()
			cancel()
			return
		}
		cur.cancel()
	}
	e.speed.floods[l.ID] = &flood{testID: testID, cancel: cancel}
	e.speed.mu.Unlock()
	go func() {
		defer cancel()
		e.flood(ctx, l, testID, clampDur(dur), clampRate(int(capMbps)))
	}()
}

// flood sends SPEED_FILL on l for dur, paced by a token bucket (rate in wire
// bytes) refilled from the wall clock and checked every ~1 ms, so a late timer
// tick is made up for in the next burst (bucket capped at 20 ms of traffic).
func (e *Engine) flood(ctx context.Context, l *Link, testID uint32, dur time.Duration, capMbps int) {
	payload := proto.PutSpeedFill(testID, speedFillSize)
	wire := float64(proto.Overhead + len(payload))
	rate := float64(capMbps) * 1e6 / 8
	bucket := rate * 0.02
	if bucket < 4*wire {
		bucket = 4 * wire
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	start := time.Now()
	deadline := start.Add(dur)
	last := start
	tokens := 2 * wire
	for {
		now := time.Now()
		if !now.Before(deadline) {
			return
		}
		tokens += rate * now.Sub(last).Seconds()
		last = now
		if tokens > bucket {
			tokens = bucket
		}
		for tokens >= wire {
			e.sealAndSend(l, proto.TypeSpeedFill, payload)
			tokens -= wire
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func newTestID() uint32 {
	var b [4]byte
	for {
		if _, err := crand.Read(b[:]); err != nil {
			return uint32(time.Now().UnixNano()) | 1
		}
		if id := binary.BigEndian.Uint32(b[:]); id != 0 {
			return id
		}
	}
}

// SpeedResult is the measured capacity of one link to the VPS, in Mbit/s.
type SpeedResult struct{ DownMbps, UpMbps float64 }

var errNoDown = errors.New("không nhận được dữ liệu từ VPS qua đường này")

// MeasureLink (client only) measures one link: a download test (the server
// floods the link for dur) then an upload test (the client floods for dur and
// asks the server how much arrived). dur is clamped to at most 5s (0 = 3s). If the
// upload half fails the download result is still returned alongside the error.
// Calls on different links may run concurrently.
func (e *Engine) MeasureLink(ctx context.Context, id uint8, dur time.Duration) (SpeedResult, error) {
	var res SpeedResult
	if e.cfg.Role != Client {
		return res, errors.New("chỉ client mới đo được tốc độ")
	}
	l := e.link(id)
	if l == nil {
		return res, fmt.Errorf("không có đường %d", id)
	}
	dur = clampDur(dur)
	capMbps := e.speedCapMbps
	if capMbps == 0 {
		capMbps = speedDefaultRate
	}
	down, err := e.measureDown(ctx, l, dur, capMbps)
	if err != nil {
		return res, err
	}
	res.DownMbps = down
	up, err := e.measureUp(ctx, l, dur, capMbps)
	if err != nil {
		return res, err
	}
	res.UpMbps = up
	return res, nil
}

// sleepCtx waits d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (e *Engine) measureDown(ctx context.Context, l *Link, dur time.Duration, capMbps uint16) (float64, error) {
	id := newTestID()
	e.speed.begin(id, false)
	defer e.speed.end(id)
	req := proto.PutSpeedReq(id, proto.SpeedDown, dur, capMbps)
	e.sealAndSend(l, proto.TypeSpeedReq, req)

	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	start := time.Now()
	resent := false
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-tick.C:
		}
		now := time.Now()
		bytes, first, last := e.speed.snapshot(id)
		if first.IsZero() {
			waited := now.Sub(start)
			if waited >= time.Second && !resent {
				resent = true
				e.sealAndSend(l, proto.TypeSpeedReq, req)
			}
			if waited >= 2*time.Second {
				return 0, errNoDown
			}
			continue
		}
		// Done when the flood has gone quiet or the window is certainly over.
		if now.Sub(last) >= 600*time.Millisecond || now.Sub(first) >= dur+time.Second {
			span := last.Sub(first)
			if bytes == 0 || span < 10*time.Millisecond {
				return 0, errors.New("nhận được quá ít dữ liệu từ VPS để đo tốc độ tải xuống")
			}
			return float64(bytes) * 8 / span.Seconds() / 1e6, nil
		}
	}
}

func (e *Engine) measureUp(ctx context.Context, l *Link, dur time.Duration, capMbps uint16) (float64, error) {
	id := newTestID()
	t := e.speed.begin(id, true)
	defer e.speed.end(id)

	e.flood(ctx, l, id, dur, int(capMbps)) // returns at the deadline or when ctx ends
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := sleepCtx(ctx, 150*time.Millisecond); err != nil { // let the tail of the flood land
		return 0, err
	}
	req := proto.PutSpeedReq(id, proto.SpeedUpResult, 0, 0)
	for try := 0; try < 3; try++ {
		e.sealAndSend(l, proto.TypeSpeedReq, req)
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case r := <-t.res:
			timer.Stop()
			if r.bytes == 0 || r.span < 10*time.Millisecond {
				return 0, errors.New("VPS không nhận đủ dữ liệu tải lên qua đường này")
			}
			return float64(r.bytes) * 8 / r.span.Seconds() / 1e6, nil
		case <-timer.C:
		}
	}
	return 0, errors.New("VPS không trả kết quả đo tốc độ tải lên")
}
