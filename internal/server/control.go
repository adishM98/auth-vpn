package server

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"time"
)

type controlRequest struct {
	Cmd string `json:"cmd"`
}

type controlResponse struct {
	Clients []clientInfo `json:"clients"`
	Error   string       `json:"error,omitempty"`
}

// startControlSocket opens a Unix domain socket at SocketFile and serves
// simple JSON queries from the CLI (e.g. "server clients").
// Must be called as a goroutine from Start().
func (s *Server) startControlSocket() {
	// Remove any stale socket from a previous run.
	_ = os.Remove(SocketFile)

	ln, err := net.Listen("unix", SocketFile)
	if err != nil {
		log.Printf("control socket: %v (server clients will not work)", err)
		return
	}
	// Restrict to root only — the server runs as root.
	_ = os.Chmod(SocketFile, 0o600)

	// Close listener when server shuts down.
	go func() {
		<-s.done
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				log.Printf("control accept: %v", err)
				continue
			}
		}
		go s.handleControl(conn)
	}
}

func (s *Server) handleControl(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck

	var req controlRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	var resp controlResponse
	switch req.Cmd {
	case "clients":
		resp.Clients = s.clients.Snapshot()
		if resp.Clients == nil {
			resp.Clients = []clientInfo{} // always return an array, never null
		}
	default:
		resp.Error = "unknown command: " + req.Cmd
	}

	json.NewEncoder(conn).Encode(resp) //nolint:errcheck
}
