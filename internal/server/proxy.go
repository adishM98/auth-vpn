package server

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/adishM98/auth-vpn/internal/tunnel"
	"github.com/adishM98/auth-vpn/pkg/protocol"
)

// handleProxyConn handles a post-auth connection that requested proxy mode.
// The client sends ProxyDial frames; we dial the target on the server's local
// network and ferry bytes back and forth over the shared TLS connection.
// No TUN device or CAP_NET_ADMIN is required on the client side.
func (s *Server) handleProxyConn(conn net.Conn, name string) {
	log.Printf("proxy client connected: %s", name)
	defer log.Printf("proxy client disconnected: %s", name)

	var streamsMu sync.Mutex
	streams := make(map[uint32]net.Conn)

	var writeMu sync.Mutex
	writeFrame := func(msgType byte, payload []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		tunnel.WriteFrame(conn, msgType, payload) //nolint:errcheck
	}

	defer func() {
		streamsMu.Lock()
		for _, c := range streams {
			c.Close()
		}
		streamsMu.Unlock()
	}()

	for {
		msgType, payload, err := tunnel.ReadFrame(conn)
		if err != nil {
			return
		}

		switch msgType {
		case protocol.TypeProxyDial:
			var req protocol.ProxyDialRequest
			if err := protocol.Decode(payload, &req); err != nil {
				continue
			}

			target := fmt.Sprintf("%s:%d", req.Host, req.Port)
			tc, err := net.DialTimeout("tcp", target, 10*time.Second)
			if err != nil {
				log.Printf("proxy: %s dial %s: %v", name, target, err)
				writeFrame(protocol.TypeProxyFail, protocol.Encode(protocol.ProxyDialFail{
					StreamID: req.StreamID,
					Reason:   err.Error(),
				}))
				continue
			}

			streamsMu.Lock()
			streams[req.StreamID] = tc
			streamsMu.Unlock()

			writeFrame(protocol.TypeProxyOK, protocol.Encode(protocol.ProxyDialOK{StreamID: req.StreamID}))
			log.Printf("proxy: %s stream %d → %s", name, req.StreamID, target)

			// remote service → client goroutine
			go func(id uint32, tc net.Conn) {
				buf := make([]byte, 32768)
				defer func() {
					tc.Close()
					streamsMu.Lock()
					delete(streams, id)
					streamsMu.Unlock()
					closePayload := make([]byte, 4)
					binary.BigEndian.PutUint32(closePayload, id)
					writeFrame(protocol.TypeProxyClose, closePayload)
				}()
				for {
					n, err := tc.Read(buf)
					if n > 0 {
						data := make([]byte, 4+n)
						binary.BigEndian.PutUint32(data[:4], id)
						copy(data[4:], buf[:n])
						writeFrame(protocol.TypeProxyData, data)
					}
					if err != nil {
						return
					}
				}
			}(req.StreamID, tc)

		case protocol.TypeProxyData:
			if len(payload) < 4 {
				continue
			}
			id := binary.BigEndian.Uint32(payload[:4])
			data := payload[4:]
			streamsMu.Lock()
			tc := streams[id]
			streamsMu.Unlock()
			if tc != nil && len(data) > 0 {
				tc.Write(data) //nolint:errcheck
			}

		case protocol.TypeProxyClose:
			if len(payload) < 4 {
				continue
			}
			id := binary.BigEndian.Uint32(payload[:4])
			streamsMu.Lock()
			tc := streams[id]
			delete(streams, id)
			streamsMu.Unlock()
			if tc != nil {
				tc.Close()
			}

		case protocol.TypePing:
			writeFrame(protocol.TypePong, nil)

		case protocol.TypeDisconnect:
			return
		}
	}
}
