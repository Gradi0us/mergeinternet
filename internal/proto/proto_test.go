package proto

import (
	"bytes"
	"testing"
	"time"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher(bytes.Repeat([]byte{7}, KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSealOpenRoundTrip(t *testing.T) {
	c := testCipher(t)
	h := Header{Type: TypeData, PathID: 2, Dir: ServerToClient, Session: 42, Counter: 9}
	payload := PutData(1234, []byte("ip packet"))
	pkt := c.Seal(nil, h, payload)
	if len(pkt) != Overhead+len(payload) {
		t.Fatalf("len = %d, want %d", len(pkt), Overhead+len(payload))
	}
	got, pt, err := c.Open(nil, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Fatalf("header = %+v, want %+v", got, h)
	}
	seq, ip, err := ParseData(pt)
	if err != nil || seq != 1234 || string(ip) != "ip packet" {
		t.Fatalf("data = %d %q %v", seq, ip, err)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	c := testCipher(t)
	pkt := c.Seal(nil, Header{Type: TypeData, Session: 1, Counter: 1}, []byte("hello"))
	for _, i := range []int{2, 5, 15, HeaderLen + 1, len(pkt) - 1} { // header (AAD) and body
		bad := append([]byte(nil), pkt...)
		bad[i] ^= 1
		if _, _, err := c.Open(nil, bad); err == nil {
			t.Errorf("tampered byte %d accepted", i)
		}
	}
	other, _ := NewCipher(bytes.Repeat([]byte{8}, KeyLen))
	if _, _, err := other.Open(nil, pkt); err == nil {
		t.Error("wrong key accepted")
	}
	if _, _, err := c.Open(nil, pkt[:Overhead-1]); err != ErrShort {
		t.Errorf("short packet: err = %v", err)
	}
}

func TestDirectionsUseDistinctNonces(t *testing.T) {
	a := nonce(Header{Dir: ClientToServer, Session: 5, Counter: 1})
	b := nonce(Header{Dir: ServerToClient, Session: 5, Counter: 1})
	if a == b {
		t.Fatal("client and server would reuse a nonce")
	}
}

func TestProbeRoundTrip(t *testing.T) {
	sent, kbps, limit, err := ParseProbe(PutProbe(-1, 40000, 2000))
	if err != nil || sent != -1 || kbps != 40000 || limit != 2000 {
		t.Fatalf("got %d %d %d %v", sent, kbps, limit, err)
	}
	if _, _, _, err := ParseProbe(make([]byte, 12)); err == nil {
		t.Fatal("old 12-byte probe accepted")
	}
}

func TestProbeAckEchoesProbeAndBoot(t *testing.T) {
	probe := PutProbe(123456789, 1000, 500)
	ack := PutProbeAck(probe, 0xdeadbeef)
	if len(ack) != 24 {
		t.Fatalf("ack len %d, want 24", len(ack))
	}
	sent, boot, err := ParseProbeAck(ack)
	if err != nil || sent != 123456789 || boot != 0xdeadbeef {
		t.Fatalf("got %d %x %v", sent, boot, err)
	}
	// The ack must still carry the probe's down limit untouched.
	if _, _, limit, err := ParseProbe(ack); err != nil || limit != 500 {
		t.Fatalf("limit %d err %v", limit, err)
	}
	if _, _, err := ParseProbeAck(ack[:20]); err == nil {
		t.Fatal("truncated ack accepted")
	}
}

func TestSpeedReqRoundTrip(t *testing.T) {
	b := PutSpeedReq(0xdeadbeef, SpeedDown, 2500*time.Millisecond, 1000)
	id, op, dur, rate, err := ParseSpeedReq(b)
	if err != nil || id != 0xdeadbeef || op != SpeedDown || dur != 2500*time.Millisecond || rate != 1000 {
		t.Fatalf("got %x %d %v %d %v", id, op, dur, rate, err)
	}
	if _, op, _, _, err := ParseSpeedReq(PutSpeedReq(1, SpeedUpResult, 0, 0)); err != nil || op != SpeedUpResult {
		t.Fatalf("op %d err %v", op, err)
	}
	if _, _, _, _, err := ParseSpeedReq(b[:len(b)-1]); err != ErrShort {
		t.Fatalf("short req: err = %v", err)
	}
}

func TestSpeedFillRoundTrip(t *testing.T) {
	b := PutSpeedFill(77, 1200)
	if len(b) != 1200 {
		t.Fatalf("len = %d, want 1200", len(b))
	}
	if id, err := ParseSpeedFill(b); err != nil || id != 77 {
		t.Fatalf("got %d %v", id, err)
	}
	if len(PutSpeedFill(1, 0)) != 4 {
		t.Fatal("fill smaller than its ID header")
	}
	if _, err := ParseSpeedFill(b[:3]); err != ErrShort {
		t.Fatalf("short fill: err = %v", err)
	}
}

func TestSpeedResRoundTrip(t *testing.T) {
	b := PutSpeedRes(9, 1<<40, 1500*time.Millisecond)
	id, n, span, err := ParseSpeedRes(b)
	if err != nil || id != 9 || n != 1<<40 || span != 1500*time.Millisecond {
		t.Fatalf("got %d %d %v %v", id, n, span, err)
	}
	if _, _, _, err := ParseSpeedRes(b[:19]); err != ErrShort {
		t.Fatalf("short res: err = %v", err)
	}
}
