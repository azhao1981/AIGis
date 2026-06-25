package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// recordingProc is a test Processor that logs its name on each call and can
// optionally rewrite the body or return an error.
type recordingProc struct {
	name     string
	priority int
	order    *[]string
	onReq    func([]byte) ([]byte, error)
	onResp   func([]byte) ([]byte, error)
}

func (p *recordingProc) Name() string  { return p.name }
func (p *recordingProc) Priority() int { return p.priority }

func (p *recordingProc) OnRequest(_ *AIGisContext, body []byte) ([]byte, error) {
	*p.order = append(*p.order, p.name)
	if p.onReq != nil {
		return p.onReq(body)
	}
	return body, nil
}

func (p *recordingProc) OnResponse(_ *AIGisContext, body []byte) ([]byte, error) {
	*p.order = append(*p.order, p.name)
	if p.onResp != nil {
		return p.onResp(body)
	}
	return body, nil
}

func TestPipeline_ExecutesInPriorityOrder(t *testing.T) {
	var order []string
	p := NewPipeline()
	// Added out of order; must run by ascending priority.
	p.AddProcessor(&recordingProc{name: "b", priority: 10, order: &order})
	p.AddProcessor(&recordingProc{name: "a", priority: -100, order: &order})
	p.AddProcessor(&recordingProc{name: "c", priority: 50, order: &order})

	ctx := NewGatewayContext(context.Background(), nil)
	if _, err := p.ExecuteRequest(ctx, []byte("{}")); err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(order, want) {
		t.Errorf("OnRequest order = %v, want %v", order, want)
	}
}

func TestPipeline_ThreadsBodyThroughStages(t *testing.T) {
	var order []string
	p := NewPipeline()
	p.AddProcessor(&recordingProc{name: "append1", priority: 0, order: &order,
		onReq: func(b []byte) ([]byte, error) { return append(b, '1'), nil }})
	p.AddProcessor(&recordingProc{name: "append2", priority: 1, order: &order,
		onReq: func(b []byte) ([]byte, error) { return append(b, '2'), nil }})

	ctx := NewGatewayContext(context.Background(), nil)
	out, err := p.ExecuteRequest(ctx, []byte("x"))
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	if string(out) != "x12" {
		t.Errorf("body = %q, want %q", out, "x12")
	}
}

func TestPipeline_ErrorShortCircuits(t *testing.T) {
	var order []string
	p := NewPipeline()
	p.AddProcessor(&recordingProc{name: "first", priority: 0, order: &order,
		onReq: func([]byte) ([]byte, error) { return nil, errors.New("boom") }})
	p.AddProcessor(&recordingProc{name: "second", priority: 10, order: &order})

	ctx := NewGatewayContext(context.Background(), nil)
	_, err := p.ExecuteRequest(ctx, []byte("{}"))
	if err == nil {
		t.Fatal("expected error from first processor")
	}
	for _, n := range order {
		if n == "second" {
			t.Error("second processor ran despite earlier error")
		}
	}
}

func TestPipeline_ResponseOrder(t *testing.T) {
	var order []string
	p := NewPipeline()
	p.AddProcessor(&recordingProc{name: "b", priority: 10, order: &order})
	p.AddProcessor(&recordingProc{name: "a", priority: 0, order: &order})

	ctx := NewGatewayContext(context.Background(), nil)
	if _, err := p.ExecuteResponse(ctx, []byte("{}")); err != nil {
		t.Fatalf("ExecuteResponse: %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(order, want) {
		t.Errorf("OnResponse order = %v, want %v", order, want)
	}
}
