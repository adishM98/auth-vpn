package server

import (
	"bufio"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
)

// dualListener serves TLS and plain HTTP on one port. Each new connection's
// first byte decides: 0x16 is a TLS handshake record, anything else is plain
// HTTP. TLS connections are returned as *tls.Conn so http.Server sets r.TLS.
// Classification runs per connection in its own goroutine, so a client that
// connects and sends nothing can't stall Accept for everyone else.
type dualListener struct {
	net.Listener
	tlsCfg *tls.Config
	conns  chan net.Conn
	errs   chan error
	closed chan struct{}
	once   sync.Once
}

func newDualListener(inner net.Listener, tlsCfg *tls.Config) *dualListener {
	l := &dualListener{Listener: inner, tlsCfg: tlsCfg,
		conns: make(chan net.Conn), errs: make(chan error, 1), closed: make(chan struct{})}
	go l.acceptLoop()
	return l
}

func (l *dualListener) acceptLoop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			l.errs <- err
			return
		}
		go l.classify(c)
	}
}

func (l *dualListener) classify(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	c.SetReadDeadline(time.Time{}) //nolint:errcheck
	if err != nil {
		c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if first[0] == 0x16 {
		out = tls.Server(out, l.tlsCfg)
	}
	select {
	case l.conns <- out:
	case <-l.closed:
		out.Close()
	}
}

func (l *dualListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errs:
		return nil, err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *dualListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

// peekedConn replays the byte(s) consumed while sniffing the protocol.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// plainHTTPOnlyFromPrivate serves unencrypted requests only when the path to
// the client is already private: loopback (e.g. kubectl port-forward, which
// arrives on the pod's localhost) or the VPN subnet (inside the TLS tunnel).
// Plain HTTP from anywhere else is redirected to https, so the API key and
// dashboard are never served in cleartext over the internet.
func (s *Server) plainHTTPOnlyFromPrivate(next http.Handler) http.Handler {
	_, vpn, _ := net.ParseCIDR(s.cfg.Subnet)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			if ip == nil || !(ip.IsLoopback() || (vpn != nil && vpn.Contains(ip))) {
				http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
