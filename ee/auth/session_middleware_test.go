// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeSessions is an in-memory sessionAPI for testing (no Redis).
type fakeSessions struct {
	mu   sync.Mutex
	data map[string]Principal
	seq  int
}

func newFakeSessions() *fakeSessions { return &fakeSessions{data: map[string]Principal{}} }

func (f *fakeSessions) New(_ context.Context, p Principal) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := "sess-" + string(rune('a'+f.seq))
	f.data[id] = p
	return id, nil
}
func (f *fakeSessions) Get(_ context.Context, id string) (Principal, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.data[id]
	return p, ok
}
func (f *fakeSessions) Delete(_ context.Context, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, id)
}

// fakeUsers is an in-memory userAPI: one hard-coded valid credential.
type fakeUsers struct{}

func (fakeUsers) VerifyPassword(_ context.Context, email, password string) (Principal, error) {
	if email == "admin@acme.io" && password == "s3cret" {
		return Principal{Tenant: "acme", Subject: email, Admin: true}, nil
	}
	return Principal{}, ErrInvalidCredentials
}

// okNext is a downstream handler that records whether it ran and echoes the
// resolved principal from context.
func okNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(p.Subject))
	})
}

// denyKeys is an AuthProvider that always rejects, so we can prove the session
// path (not the Bearer fallback) admitted a request.
type denyKeys struct{}

func (denyKeys) Authenticate(*http.Request) (Principal, error) { return Principal{}, ErrUnauthorized }

func TestLoginSetsCookieAndSessionWorks(t *testing.T) {
	sessions := newFakeSessions()
	h := SessionMiddleware(fakeUsers{}, sessions, denyKeys{}, nil)(okNext())

	// 1. login with good credentials -> 200 + Set-Cookie
	body := strings.NewReader(`{"email":"admin@acme.io","password":"s3cret"}`)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", rec.Code)
	}
	cookies := rec.Result().Cookies()
	var sess *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil || sess.Value == "" {
		t.Fatalf("login did not set %s cookie", sessionCookie)
	}

	// 2. a protected request carrying the cookie is admitted (denyKeys would 401
	// the Bearer path, so success proves the session path worked).
	req2 := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	req2.AddCookie(sess)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("session request status = %d, want 200", rec2.Code)
	}
	if got := rec2.Body.String(); got != "admin@acme.io" {
		t.Fatalf("principal subject = %q, want admin@acme.io", got)
	}
}

func TestLoginWrongPassword401(t *testing.T) {
	h := SessionMiddleware(fakeUsers{}, newFakeSessions(), denyKeys{}, nil)(okNext())
	body := strings.NewReader(`{"email":"admin@acme.io","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNoCredentialsRejected(t *testing.T) {
	h := SessionMiddleware(fakeUsers{}, newFakeSessions(), denyKeys{}, nil)(okNext())
	req := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no cookie, no bearer)", rec.Code)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	sessions := newFakeSessions()
	h := SessionMiddleware(fakeUsers{}, sessions, denyKeys{}, nil)(okNext())

	id, _ := sessions.New(context.Background(), Principal{Tenant: "acme", Subject: "admin@acme.io"})
	cookie := &http.Cookie{Name: sessionCookie, Value: id}

	// logout deletes the session
	reqOut := httptest.NewRequest(http.MethodPost, "/logout", nil)
	reqOut.AddCookie(cookie)
	recOut := httptest.NewRecorder()
	h.ServeHTTP(recOut, reqOut)
	if recOut.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", recOut.Code)
	}

	// the same cookie is now rejected
	req := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout status = %d, want 401", rec.Code)
	}
}

func TestMeReturnsPrincipal(t *testing.T) {
	sessions := newFakeSessions()
	h := SessionMiddleware(fakeUsers{}, sessions, denyKeys{}, nil)(okNext())
	id, _ := sessions.New(context.Background(), Principal{Tenant: "acme", Subject: "admin@acme.io", Admin: true})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: id})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me status = %d, want 200", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["subject"] != "admin@acme.io" || m["admin"] != true {
		t.Fatalf("/me body = %v", m)
	}
}

func TestMeUnauthenticated401(t *testing.T) {
	h := SessionMiddleware(fakeUsers{}, newFakeSessions(), denyKeys{}, nil)(okNext())
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/me status = %d, want 401", rec.Code)
	}
}

func TestBearerFallbackWhenNoSession(t *testing.T) {
	// A provider that accepts a specific token, to prove the Bearer fallback
	// still works when there is no session cookie.
	keys := NewStaticAPIKeyProvider(map[string]string{"tok-123": "acme"})
	h := SessionMiddleware(fakeUsers{}, newFakeSessions(), keys, nil)(okNext())

	req := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer tok-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer fallback status = %d, want 200", rec.Code)
	}
}
