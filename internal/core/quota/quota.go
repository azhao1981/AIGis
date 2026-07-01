// Package quota defines the gateway's per-tenant admission seam. After a request
// is authenticated, the core asks a quota.Limiter whether the tenant may proceed;
// on denial the request is rejected with HTTP 429. The open-source build uses
// AllowAll (a no-op that never rejects), so the feature is inert unless the
// Enterprise Edition injects its own Limiter via server.SetQuotaLimiter — a
// one-way ee -> core extension, mirroring the auth and usage seams.
package quota

// Release frees a slot reserved by a successful Acquire. Acquire returns a
// Release func the caller MUST invoke exactly once (e.g. via defer) when it was
// admitted. For a rejected request the returned Release is a safe no-op.
type Release func()

// Limiter gates requests per tenant. Acquire reports whether the given tenant is
// admitted right now; when admitted it returns a Release to free the reserved
// slot at request end. Implementations must be safe for concurrent use.
type Limiter interface {
	Acquire(tenant string) (Release, bool)
}

// noop is a Limiter that admits everything. It is the open-source default.
type noop struct{}

// Acquire always admits and returns a no-op Release.
func (noop) Acquire(string) (Release, bool) { return func() {}, true }

// AllowAll returns the no-op Limiter used by the open-source build.
func AllowAll() Limiter { return noop{} }
