package engine

import (
	"context"
	"fmt"
	"net"
	"time"
)

// SocketBuffer is the kernel send/receive buffer requested for tunnel sockets.
// OS defaults (~64 KiB on Windows) overflow during bursts at 100+ Mbps,
// silently dropping datagrams. Linux caps it at net.core.rmem_max/wmem_max.
const SocketBuffer = 4 << 20

func SetSocketBuffers(c *net.UDPConn) {
	_ = c.SetReadBuffer(SocketBuffer)
	_ = c.SetWriteBuffer(SocketBuffer)
}

// LogStats prints per-link throughput every interval until ctx is done.
func (e *Engine) LogStats(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	prev := map[uint8]LinkStats{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		links, rs, bad := e.Stats()
		var totTx, totRx float64
		for _, l := range links {
			p := prev[l.ID]
			tx := mbps(l.TxBytes-p.TxBytes, every)
			rx := mbps(l.RxBytes-p.RxBytes, every)
			totTx += tx
			totRx += rx
			state := "DOWN"
			if l.Alive {
				state = "up"
			}
			extra := ""
			if l.Remote != "" {
				extra = " from " + l.Remote
			}
			if l.TxErrors > p.TxErrors {
				extra += fmt.Sprintf(" txerr+%d", l.TxErrors-p.TxErrors)
			}
			e.cfg.Logf("  [%d] %-10s %-4s rtt=%-6s tx=%6.1f Mbps rx=%6.1f Mbps%s",
				l.ID, l.Name, state, l.RTT.Round(time.Millisecond), tx, rx, extra)
			prev[l.ID] = l
		}
		e.cfg.Logf("  total     tx=%6.1f Mbps rx=%6.1f Mbps | delivered=%d late=%d lost=%d dup=%d bad=%d dropped=%d",
			totTx, totRx, rs.Delivered, rs.Late, rs.Skipped, rs.Duplicates, bad, e.Dropped())
	}
}

func mbps(bytes uint64, d time.Duration) float64 {
	return float64(bytes) * 8 / d.Seconds() / 1e6
}
