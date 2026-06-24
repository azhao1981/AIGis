package server_test

import (
	"bufio"
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"aigis/internal/pkg/logger"
	"aigis/internal/server"
)

// placeholderRe matches AIGis tokenization placeholders.
var placeholderRe = regexp.MustCompile(`__AIGIS_SEC_[0-9a-f]{12}__`)

// newMockUpstream returns an SSE upstream that echoes back, in a streamed
// chunk, any tokenization placeholder it finds in the (already-masked) request
// body. This lets the test verify the full mask -> vault -> unmask round-trip
// over the streaming path without replicating the placeholder hashing.
func newMockUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body)
		placeholder := placeholderRe.FindString(body.String())
		if placeholder == "" {
			t.Errorf("upstream received no placeholder; masking did not run. body=%s", body.String())
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream ResponseWriter is not a Flusher")
		}
		// Stream the placeholder back inside an SSE data line, then the OpenAI
		// terminating sentinel, mirroring real provider framing.
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + placeholder + "\"}}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
}

func TestStreamUnmaskRoundTrip(t *testing.T) {
	mock := newMockUpstream(t)
	defer mock.Close()

	// Configure a single route pointing at the mock upstream, matching test-* models.
	viper.Set("engine.routes", []map[string]any{
		{
			"id":      "test-stream",
			"matcher": map[string]string{"model": "^test-.*"},
			"upstream": map[string]any{
				"base_url":      mock.URL,
				"path":          "/v1/chat/completions",
				"auth_strategy": "none",
			},
			"transforms": []map[string]any{
				{"type": "pii", "config": map[string]string{}},
			},
		},
	})
	defer viper.Set("engine.routes", nil)

	zapLog, _ := logger.New("error")
	srv, err := server.NewHTTPServer(":0", zapLog)
	if err != nil {
		t.Fatalf("NewHTTPServer failed: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const secret = "secret@example.com"
	reqBody := `{"model":"test-1","stream":true,"messages":[{"role":"user","content":"my email is ` + secret + `"}]}`

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected SSE content-type, got %q", ct)
	}

	out := new(bytes.Buffer)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		out.WriteString(scanner.Text())
		out.WriteString("\n")
	}
	got := out.String()

	// The original secret must be restored in the streamed output...
	if !strings.Contains(got, secret) {
		t.Errorf("expected secret %q restored in stream, got:\n%s", secret, got)
	}
	// ...and no placeholder should leak to the client.
	if placeholderRe.MatchString(got) {
		t.Errorf("placeholder leaked to client in stream:\n%s", got)
	}
	// SSE framing preserved.
	if !strings.Contains(got, "data: ") || !strings.Contains(got, "[DONE]") {
		t.Errorf("SSE framing not preserved:\n%s", got)
	}
}
