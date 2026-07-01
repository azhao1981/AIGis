package server

import "net/http"

// Middleware wraps an http.Handler to add cross-cutting behavior (auth, quota,
// tenant resolution, ...) around the gateway. It is the single extension point
// the Enterprise Edition (ee/) plugs into; the open-source core ships with an
// empty chain, so default behavior is unchanged.
type Middleware func(http.Handler) http.Handler

// Use appends middlewares to the server's chain. They wrap the mux in the order
// given: the first Use'd runs outermost (closest to the client). Call before
// Start/Handler. No-op for the open-source build, which registers none.
func (s *HTTPServer) Use(mw ...Middleware) {
	s.middlewares = append(s.middlewares, mw...)
}

// buildHandler applies the middleware chain around the mux. With no middlewares
// it returns the bare mux, so the open-source path is byte-for-byte unchanged.
func (s *HTTPServer) buildHandler() http.Handler {
	var h http.Handler = s.mux
	for i := len(s.middlewares) - 1; i >= 0; i-- {
		h = s.middlewares[i](h)
	}
	return h
}
