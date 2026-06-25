package processors

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"aigis/internal/core"
)

func TestRequestLogger_PassesBodyThrough(t *testing.T) {
	p := NewRequestLogger()
	if p.Name() != "request-logger" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.Priority() != -100 {
		t.Errorf("Priority = %d, want -100 (runs first)", p.Priority())
	}

	ctx := core.NewGatewayContext(context.Background(), zap.NewNop())
	body := []byte(`{"model":"glm-4.6","messages":[]}`)

	// The logger is observational: it must return the body unchanged.
	out, err := p.OnRequest(ctx, body)
	if err != nil {
		t.Fatalf("OnRequest: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("OnRequest mutated body: got %q", out)
	}

	out, err = p.OnResponse(ctx, body)
	if err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("OnResponse mutated body: got %q", out)
	}
}
