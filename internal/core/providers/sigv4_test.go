package providers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"aigis/internal/core"
	"aigis/internal/core/engine"
)

// TestSigV4_SignatureMatchesServerResign is the end-to-end check: the gateway
// signs a POST with the Bedrock strategy; the mock upstream re-derives the
// signature from the received request using the SAME algorithm and compares.
// A mismatch would mean our client-side canonicalization produced something
// the server cannot reconstruct from the wire.
func TestSigV4_SignatureMatchesServerResign(t *testing.T) {
	const accessKey = "AKIAFAKEACCESSKEYEXAMPLE"
	const secretKey = "fakeSecretAccessKeyForBedrockTest123456"
	const region = "us-east-1"

	t.Setenv("AIGIS_BEDROCK_AK", accessKey)
	t.Setenv("AIGIS_BEDROCK_SK", secretKey)

	var gotAuthz, gotAmzDate, gotBodyHash string
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthz = r.Header.Get("Authorization")
		gotAmzDate = r.Header.Get("X-Amz-Date")
		gotBodyHash = r.Header.Get("X-Amz-Content-SHA256")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"output":{"message":{"content":[{"text":"ok"}]}}}`))
	}))
	defer up.Close()

	// Parse the mock URL so the server-side re-sign mirrors what a real
	// Bedrock endpoint would verify against.
	parsed, _ := url.Parse(up.URL)

	route := &engine.Route{
		ID:      "bedrock",
		Matcher: map[string]string{"model": "^anthropic.claude"},
		Upstream: engine.Upstream{
			BaseURL:      parsed.Scheme + "://" + parsed.Host,
			Path:         "/model/anthropic.claude-3-sonnet/v1/invoke",
			AuthStrategy: engine.AuthStrategyBedrock,
			Region:       region,
			AccessKeyEnv: "AIGIS_BEDROCK_AK",
			SecretKeyEnv: "AIGIS_BEDROCK_SK",
		},
	}
	p := NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	body := []byte(`{"anthropic_version":"bedrock-2023-05-31","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`)
	if _, err := p.Send(ctx, body, http.Header{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Body hash header must match SHA256 of the actual body we sent.
	wantBodyHash := sha256Hex(gotBody)
	if gotBodyHash != wantBodyHash {
		t.Errorf("X-Amz-Content-SHA256 mismatch: header=%s actual=%s", gotBodyHash, wantBodyHash)
	}

	// Re-sign the EXACT wire request (same method, URL path, query, body, time)
	// and expect an identical Authorization header.
	parsedPath, _ := url.Parse(up.URL + route.Upstream.Path)
	signTime, _ := time.Parse("20060102T150405Z", gotAmzDate)
	srvHeaders, err := signBedrockSigV4(http.MethodPost, parsedPath, gotBody, accessKey, secretKey, region, signTime)
	if err != nil {
		t.Fatalf("server-side resign failed: %v", err)
	}
	if srvHeaders.Get("Authorization") != gotAuthz {
		t.Errorf("SigV4 signature mismatch:\n client: %s\n server: %s",
			gotAuthz, srvHeaders.Get("Authorization"))
	}

	// Sanity: Authorization header has the expected structural pieces.
	for _, want := range []string{
		"AWS4-HMAC-SHA256",
		"Credential=" + accessKey + "/",
		"/" + region + "/bedrock/aws4_request",
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date",
	} {
		if !strings.Contains(gotAuthz, want) {
			t.Errorf("Authorization missing %q: %s", want, gotAuthz)
		}
	}
}

// TestSigV4_KnownVector verifies the HMAC-SHA256 signing key derivation chain
// against the AWS published example. The canonical input ("AWS4" + secret,
// date, region, service, "aws4_request") is deterministic regardless of the
// canonical-request step, so we can validate the chain directly with the
// doc example values.
//
// Source: AWS SigV4 reference example uses
//
//	secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
//	date   = "20150830"
//	region = "us-east-1"
//	service= "iam"
//
// Expected kSigning (hex):
//
//	c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9
func TestSigV4_KnownVector(t *testing.T) {
	// This test uses "iam" rather than "bedrock", so we cannot reuse the
	// package's signBedrockSigV4 (which hardcodes the bedrock service). We
	// replicate just the key-derivation chain to validate the HMAC plumbing.
	secret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	date := "20150830"
	region := "us-east-1"
	service := "iam"
	want := "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"

	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	got := hex.EncodeToString(kSigning)
	if got != want {
		t.Errorf("kSigning mismatch:\n got:  %s\n want: %s", got, want)
	}
	// Confirm hmacSHA256 == crypto/hmac + sha256 (defensive — catches a
	// future refactor that swaps in a different hash).
	mac := hmac.New(sha256.New, kService)
	mac.Write([]byte("aws4_request"))
	if hex.EncodeToString(mac.Sum(nil)) != want {
		t.Error("hmacSHA256 diverges from crypto/hmac+sha256")
	}
}

// TestSigV4_CanonicalQuery covers the percent-encoding rules (space -> %20,
// not "+"; reserved chars encoded; unreserved pass through). A wrong encoder
// would make the server-side canonical form differ and the signature reject.
func TestSigV4_CanonicalQuery(t *testing.T) {
	q := url.Values{}
	q.Set("b", "2")
	q.Set("a", "1 2") // space must encode as %20
	q.Set("c", "x/y") // slash is reserved -> %2F
	got := canonicalQuery(q)
	want := "a=1%202&b=2&c=x%2Fy"
	if got != want {
		t.Errorf("canonicalQuery:\n got:  %s\n want: %s", got, want)
	}
}

// TestSigV4_CanonicalPath covers the empty-path -> "/" rule (a real Bedrock
// invoke path has no reserved chars so it passes through unchanged).
func TestSigV4_CanonicalPath(t *testing.T) {
	if got := canonicalPath(""); got != "/" {
		t.Errorf("canonicalPath(\"\") = %q, want %q", got, "/")
	}
	const path = "/model/anthropic.claude-3-sonnet/v1/invoke"
	if got := canonicalPath(path); got != path {
		t.Errorf("canonicalPath normal path changed: %q", got)
	}
}

// TestBedrockAuth_MisconfigFailLoud asserts that a missing region or missing
// credentials surface as an error before any upstream call, rather than
// silently producing an unsigned request.
func TestBedrockAuth_MisconfigFailLoud(t *testing.T) {
	cases := []struct {
		name   string
		route  engine.Route
		setEnv func()
	}{
		{
			name: "missing region",
			route: engine.Route{ID: "b", Upstream: engine.Upstream{
				AuthStrategy: engine.AuthStrategyBedrock,
			}},
			setEnv: func() {},
		},
		{
			name: "missing env var names",
			route: engine.Route{ID: "b", Upstream: engine.Upstream{
				AuthStrategy: engine.AuthStrategyBedrock, Region: "us-east-1",
			}},
			setEnv: func() {},
		},
		{
			name: "empty credentials",
			route: engine.Route{ID: "b", Upstream: engine.Upstream{
				AuthStrategy: engine.AuthStrategyBedrock, Region: "us-east-1",
				AccessKeyEnv: "AIGIS_TEST_BEDROCK_AK", SecretKeyEnv: "AIGIS_TEST_BEDROCK_SK",
			}},
			setEnv: func() {
				t.Setenv("AIGIS_TEST_BEDROCK_AK", "")
				t.Setenv("AIGIS_TEST_BEDROCK_SK", "")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setEnv()
			p := NewUniversalProvider(&tc.route, nil, nil)
			_, err := p.Send(core.NewGatewayContext(context.Background(), nil),
				[]byte(`{}`), http.Header{})
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}
