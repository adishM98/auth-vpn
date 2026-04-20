package server

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics holds server-wide counters. All fields are updated atomically.
type Metrics struct {
	startTime      time.Time
	connectedTotal atomic.Int64 // monotonically increasing connection count
	authFailures   atomic.Int64
	activeConns    atomic.Int64 // current live connections
	bytesIn        atomic.Int64 // total bytes received from all clients → TUN
	bytesOut       atomic.Int64 // total bytes sent from TUN → all clients
	droppedPackets atomic.Int64 // sendCh-full drops
}

func newMetrics() *Metrics {
	return &Metrics{startTime: time.Now()}
}

func (m *Metrics) IncConnected()      { m.connectedTotal.Add(1); m.activeConns.Add(1) }
func (m *Metrics) DecConnected()      { m.activeConns.Add(-1) }
func (m *Metrics) IncAuthFailure()    { m.authFailures.Add(1) }
func (m *Metrics) AddBytesIn(n int)   { m.bytesIn.Add(int64(n)) }
func (m *Metrics) AddBytesOut(n int)  { m.bytesOut.Add(int64(n)) }
func (m *Metrics) IncDropped()        { m.droppedPackets.Add(1) }

// WritePrometheus writes all metrics in Prometheus text exposition format.
func (m *Metrics) WritePrometheus(w io.Writer) {
	uptime := time.Since(m.startTime).Seconds()

	writef := func(help, typ, name string, val interface{}) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, val)
	}

	writef("Server uptime in seconds", "gauge", "auth_vpn_uptime_seconds", uptime)
	writef("Current active tunnel connections", "gauge", "auth_vpn_active_connections", m.activeConns.Load())
	writef("Total tunnel connections ever established", "counter", "auth_vpn_connections_total", m.connectedTotal.Load())
	writef("Total authentication failures", "counter", "auth_vpn_auth_failures_total", m.authFailures.Load())
	writef("Total bytes received from clients (client→TUN)", "counter", "auth_vpn_bytes_in_total", m.bytesIn.Load())
	writef("Total bytes sent to clients (TUN→client)", "counter", "auth_vpn_bytes_out_total", m.bytesOut.Load())
	writef("Total packets dropped due to full send buffer", "counter", "auth_vpn_dropped_packets_total", m.droppedPackets.Load())
}
