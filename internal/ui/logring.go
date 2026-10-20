package ui

import (
	"strings"
	"sync"
)

type LogLine struct {
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
}

// LogRing keeps the last lines written to the standard logger so the UI can show them.
type LogRing struct {
	mu      sync.Mutex
	lines   []LogLine
	next    int64
	max     int
	partial string
}

func NewLogRing(max int) *LogRing { return &LogRing{max: max, next: 1} }

func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	text := r.partial + string(p)
	parts := strings.Split(text, "\n")
	r.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		r.lines = append(r.lines, LogLine{Seq: r.next, Text: strings.TrimRight(line, "\r")})
		r.next++
	}
	if over := len(r.lines) - r.max; over > 0 {
		r.lines = append(r.lines[:0:0], r.lines[over:]...)
	}
	return len(p), nil
}

// Since returns lines with Seq > seq.
func (r *LogRing) Since(seq int64) []LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []LogLine{}
	for _, l := range r.lines {
		if l.Seq > seq {
			out = append(out, l)
		}
	}
	return out
}
