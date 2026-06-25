package providers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"aigis/internal/core"
	"aigis/internal/core/engine"
	"aigis/internal/core/providers"
)

var placeholderRe = regexp.MustCompile(`__AIGIS_SEC_[0-9a-f]{12}__`)

func TestSend_MaskUnmaskRoundTrip(t *testing.T) {
	var upstreamBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		// Echo the masked placeholder back in an OpenAI-shaped response.
		ph := placeholderRe.FindString(upstreamBody)
		w.Write([]byte(`{"choices":[{"message":{"content":"` + ph + `"}}]}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:         "t",
		Upstream:   engine.Upstream{BaseURL: up.URL, Path: "/v1/chat/completions", AuthStrategy: "none"},
		Transforms: []engine.TransformStep{{Type: "pii", Config: map[string]string{}}},
	}
	p := providers.NewUniversalProvider(route, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	const secret = "secret@example.com"
	body := []byte(`{"model":"t","messages":[{"role":"user","content":"my email is ` + secret + `"}]}`)
	resp, err := p.Send(ctx, body, http.Header{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Upstream must receive a placeholder, never the plaintext secret.
	if strings.Contains(upstreamBody, secret) {
		t.Errorf("secret leaked to upstream: %s", upstreamBody)
	}
	if !placeholderRe.MatchString(upstreamBody) {
		t.Errorf("upstream got no placeholder; masking did not run: %s", upstreamBody)
	}
	// Response must have the secret restored for the client.
	if !strings.Contains(string(resp), secret) {
		t.Errorf("secret not restored in response: %s", resp)
	}
	if placeholderRe.MatchString(string(resp)) {
		t.Errorf("placeholder leaked to client: %s", resp)
	}
}

func TestSend_HeaderPolicyAndBearerAuth(t *testing.T) {
	t.Setenv("AIGIS_TEST_TOKEN", "tok-123")

	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID: "h",
		Upstream: engine.Upstream{
			BaseURL: up.URL, Path: "/x", AuthStrategy: "bearer", TokenEnv: "AIGIS_TEST_TOKEN",
		},
		HeaderPolicy: engine.HeaderPolicy{
			Allow:  []string{"X-Keep"},
			Set:    map[string]string{"X-Set": "setval"},
			Remove: []string{"X-Remove"},
		},
	}
	p := providers.NewUniversalProvider(route, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	in := http.Header{}
	in.Set("X-Keep", "keepval")
	in.Set("X-Remove", "rmval")
	in.Set("X-Drop", "dropval") // not in allow-list

	if _, err := p.Send(ctx, []byte(`{}`), in); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got.Get("Authorization") != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", got.Get("Authorization"))
	}
	if got.Get("X-Keep") != "keepval" {
		t.Errorf("allowed header X-Keep = %q, want keepval", got.Get("X-Keep"))
	}
	if got.Get("X-Set") != "setval" {
		t.Errorf("set header X-Set = %q, want setval", got.Get("X-Set"))
	}
	if got.Get("X-Remove") != "" {
		t.Errorf("removed header X-Remove should be absent, got %q", got.Get("X-Remove"))
	}
	if got.Get("X-Drop") != "" {
		t.Errorf("non-allowed header X-Drop should be dropped, got %q", got.Get("X-Drop"))
	}
}

func TestSend_UpstreamErrorSurfaces(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:       "e",
		Upstream: engine.Upstream{BaseURL: up.URL, Path: "/x", AuthStrategy: "none"},
	}
	p := providers.NewUniversalProvider(route, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	_, err := p.Send(ctx, []byte(`{"model":"e"}`), http.Header{})
	if err == nil {
		t.Fatal("expected error for non-200 upstream, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry upstream message, got %v", err)
	}
}
