package engine

import (
	"testing"
)

// FuzzFindRoute feeds arbitrary bytes into the routing path (sonic JSON parse +
// JSON-path extraction + regex matching). This is the first thing the gateway
// does with an untrusted request body, so any panic is a denial-of-service.
func FuzzFindRoute(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{}`),
		[]byte(`{"model":123}`),
		[]byte(`{"model":{"nested":"gpt-4"}}`),
		[]byte(`{"model":null}`),
		[]byte(`[]`),
		[]byte(`"just a string"`),
		[]byte(`{"model":"` + string(make([]byte, 100)) + `"}`),
		[]byte("not json at all"),
		[]byte(""),
		[]byte("\x00\xff\xfe"),
		[]byte(`{"a":{"b":{"c":{"d":{"e":"deep"}}}}}`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	eng, err := NewEngine(&EngineConfig{Routes: []Route{
		{ID: "gpt", Matcher: map[string]string{"model": "^gpt-.*"}},
		{ID: "nested", Matcher: map[string]string{"a.b.c.d.e": "deep"}},
		{ID: "fallback", Matcher: map[string]string{}},
	}})
	if err != nil {
		f.Fatalf("NewEngine: %v", err)
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		_, _ = eng.FindRoute(body)
	})
}
