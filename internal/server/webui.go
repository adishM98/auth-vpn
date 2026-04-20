package server

import (
	_ "embed"
	"net/http"
)

//go:embed ui/index.html
var uiHTML []byte

func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(uiHTML) //nolint:errcheck
}
