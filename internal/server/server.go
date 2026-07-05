package server

import (
	"net/http"
)

// Server holds the listen address and the underlying http.Server. It is
// embedded by HTTPServer, whose Start() owns the full serve/shutdown flow.
type Server struct {
	addr   string
	server *http.Server
}

// New creates a new Server instance
func New(addr string) *Server {
	return &Server{
		addr: addr,
	}
}
