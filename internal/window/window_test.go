package window

import "testing"

func TestWindow(t *testing.T) {
	var w Window
	for _, v := range []uint64{1, 3, 2, 10} {
		if !w.Check(v) {
			t.Fatalf("first %d rejected", v)
		}
	}
	for _, v := range []uint64{1, 2, 3, 10} {
		if w.Check(v) {
			t.Fatalf("duplicate %d accepted", v)
		}
	}
	if !w.Check(10 + Size*2) {
		t.Fatal("far-ahead value rejected")
	}
	if w.Check(11) {
		t.Fatal("value older than the window accepted")
	}
	if !w.Check(10 + Size*2 - 100) {
		t.Fatal("in-window out-of-order value rejected")
	}
	w.Reset()
	if !w.Check(1) {
		t.Fatal("value rejected after reset")
	}
}
