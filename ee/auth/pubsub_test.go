package auth

import (
	"context"
	"testing"
)

// TestPublishNilPublisherNoop verifies that a provider with broadcasting
// disabled (nil publisher — the default) can publish without panicking or
// erroring: key changes must still succeed when pub/sub is off.
func TestPublishNilPublisherNoop(t *testing.T) {
	p := &PostgresAPIKeyProvider{} // pub == nil
	// Must not panic and must be a no-op.
	p.publish(context.Background(), "create")
	p.publish(context.Background(), "revoke")
}

// TestSetPublisherToggles verifies SetPublisher wires and clears the publisher.
// A nil argument disables broadcasting (leaving publish a no-op).
func TestSetPublisherToggles(t *testing.T) {
	p := &PostgresAPIKeyProvider{}
	if p.pub != nil {
		t.Fatal("publisher should start nil (broadcast off by default)")
	}
	p.SetPublisher(nil)
	if p.pub != nil {
		t.Fatal("SetPublisher(nil) must leave broadcasting disabled")
	}
	// publish stays a no-op with a nil publisher.
	p.publish(context.Background(), "create")
}

// TestStartSubscribeNilClientNoGoroutine verifies StartSubscribe with a nil
// Redis client is inert: it starts no goroutine, so Close/wg must not block.
func TestStartSubscribeNilClientNoGoroutine(t *testing.T) {
	p := &PostgresAPIKeyProvider{done: make(chan struct{})}
	p.StartSubscribe(context.Background(), nil) // no client -> no goroutine
	p.wg.Wait()                                 // must return immediately
}
