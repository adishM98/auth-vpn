package server

import (
	"errors"
	"log"
	"net"
	"time"
)

// preAuthTimeout bounds the TLS/SSH handshake plus the first auth exchange.
var preAuthTimeout = 15 * time.Second

const (
	// maxPreAuth caps connections that have not authenticated yet, per listener.
	// Past it, new connections are closed immediately instead of queuing forever.
	maxPreAuth = 256
	// defaultMaxSessionsPerClient caps TUN leases one name can hold, so a single
	// token or whitelist entry can't take the whole 253-address pool.
	defaultMaxSessionsPerClient = 32
)

// acceptLoop accepts connections until ln is closed, handing each one to
// handle together with a release func that frees its pre-auth slot. Accept
// errors back off instead of spinning (e.g. when file descriptors run out).
func acceptLoop(ln net.Listener, slots chan struct{}, done <-chan struct{}, handle func(net.Conn, func())) {
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay *= 2; delay > time.Second {
				delay = time.Second
			}
			log.Printf("accept error: %v; retrying in %v", err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		select {
		case slots <- struct{}{}:
		default:
			conn.Close() // too many unauthenticated connections
			continue
		}
		released := false
		release := func() {
			if !released {
				released = true
				<-slots
			}
		}
		go handle(conn, release)
	}
}
