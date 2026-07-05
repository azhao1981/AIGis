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

	"aigis/internal/server"
)

// fakeEmailUsers is an in-memory emailUserAPI (no Postgres). It tracks pending
// registrations, enabled state, passwords, tenants, admin flags, and the tokens
// issued per email+purpose so the email flows can be driven end-to-end.
type fakeEmailUsers struct {
	mu       sync.Mutex
	pending  map[string]bool   // email -> exists (created via RegisterUserPending)
	enabled  map[string]bool   // email -> enabled
	password map[string]string // email -> current password
	tenant   map[string]string // email -> tenant
	admin    map[string]bool   // email -> is_admin
	tokens   map[string]string // token -> "email|purpose"
	seq      int
}

func newFakeEmailUsers() *fakeEmailUsers {
	return &fakeEmailUsers{
		pending:  map[string]bool{},
		enabled:  map[string]bool{},
		password: map[string]string{},
		tenant:   map[string]string{},
		admin:    map[string]bool{},
		tokens:   map[string]string{},
	}
}

func (f *fakeEmailUsers) RegisterUserPending(_ context.Context, email, password, tenant string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = normalizeEmail(email)
	if f.pending[email] {
		return ErrEmailTaken
	}
	f.pending[email] = true
	f.enabled[email] = false
	f.password[email] = password
	f.tenant[email] = tenant
	f.admin[email] = false
	return nil
}

func (f *fakeEmailUsers) IssueToken(_ context.Context, email, purpose string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = normalizeEmail(email)
	f.seq++
	tok := "tok-" + purpose + "-" + string(rune('a'+f.seq))
	f.tokens[tok] = email + "|" + purpose
	return tok, nil
}

func (f *fakeEmailUsers) ConsumeToken(_ context.Context, token, purpose string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.tokens[token]
	if !ok {
		return "", ErrTokenInvalid
	}
	parts := strings.SplitN(v, "|", 2)
	if len(parts) != 2 || parts[1] != purpose {
		return "", ErrTokenInvalid
	}
	delete(f.tokens, token)
	return parts[0], nil
}

func (f *fakeEmailUsers) ActivateUser(_ context.Context, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled[normalizeEmail(email)] = true
	return nil
}

func (f *fakeEmailUsers) UpdatePassword(_ context.Context, email, newPassword string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = normalizeEmail(email)
	if !f.pending[email] {
		return ErrUserNotFound
	}
	f.password[email] = newPassword
	return nil
}

func (f *fakeEmailUsers) SetUserAdmin(_ context.Context, email, tenantScope string, admin bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = normalizeEmail(email)
	if !f.pending[email] {
		return ErrUserNotFound
	}
	if tenantScope != "" && f.tenant[email] != tenantScope {
		return ErrUserNotFound
	}
	f.admin[email] = admin
	return nil
}

// fakeMailer captures the last message instead of dialing SMTP.
type fakeMailer struct {
	mu      sync.Mutex
	sent    int
	lastTo  string
	lastSub string
	lastBdy string
}

func (m *fakeMailer) Send(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent++
	m.lastTo, m.lastSub, m.lastBdy = to, subject, body
	return nil
}
func (*fakeMailer) Enabled() bool { return true }

// tokenFromLink pulls the ?token= value out of a captured mail body.
func tokenFromLink(body string) string {
	const marker = "token="
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.IndexAny(rest, "\r\n "); j >= 0 {
		return rest[:j]
	}
	return rest
}

// emailChain builds EmailMiddleware wrapping a terminal okNext (no auth needed
// for the public flows).
func emailChain(users emailUserAPI, mailer Mailer, opts EmailOptions) http.Handler {
	return EmailMiddleware(users, mailer, opts, nil)(okNext())
}

func doJSON(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestRegisterVerifyFlow: /register (verify on) creates a disabled account,
// mails a link, and 202s; then /verify with that link's token activates it.
func TestRegisterVerifyFlow(t *testing.T) {
	users := newFakeEmailUsers()
	mailer := &fakeMailer{}
	opts := EmailOptions{Verify: true, BaseURL: "https://app.example.com"}
	h := emailChain(users, mailer, opts)

	rec := doJSON(h, http.MethodPost, "/register",
		`{"email":"New@Acme.io","password":"pw123456","tenant":"acme"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("register(verify) status = %d, want 202", rec.Code)
	}
	if mailer.sent != 1 || mailer.lastTo != "new@acme.io" {
		t.Fatalf("verification mail not sent to normalized addr: sent=%d to=%q", mailer.sent, mailer.lastTo)
	}
	if users.enabled["new@acme.io"] {
		t.Fatal("account must stay disabled until verified")
	}
	token := tokenFromLink(mailer.lastBdy)
	if token == "" {
		t.Fatalf("no token in mail body: %q", mailer.lastBdy)
	}

	rec = doJSON(h, http.MethodGet, "/verify?token="+token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status = %d, want 200", rec.Code)
	}
	if !users.enabled["new@acme.io"] {
		t.Fatal("account should be enabled after verify")
	}
}

// TestVerifyInvalidToken400: an unknown token is refused.
func TestVerifyInvalidToken400(t *testing.T) {
	h := emailChain(newFakeEmailUsers(), &fakeMailer{}, EmailOptions{Verify: true})
	rec := doJSON(h, http.MethodGet, "/verify?token=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("verify bogus status = %d, want 400", rec.Code)
	}
}

// TestRegisterPassthroughWhenVerifyOff: with Verify=false the middleware does
// not own /register and passes through to next (okNext -> 200).
func TestRegisterPassthroughWhenVerifyOff(t *testing.T) {
	h := emailChain(newFakeEmailUsers(), &fakeMailer{}, EmailOptions{Verify: false})
	rec := doJSON(h, http.MethodPost, "/register", `{"email":"x@y.z","password":"pw","tenant":"t"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("register passthrough status = %d, want 200 (okNext)", rec.Code)
	}
}

// TestForgotAlwaysOK: /forgot returns 200 whether or not the email exists, and
// mails a reset link when a token is issued (no existence probe).
func TestForgotAlwaysOK(t *testing.T) {
	users := newFakeEmailUsers()
	mailer := &fakeMailer{}
	h := emailChain(users, mailer, EmailOptions{BaseURL: "https://app.example.com"})

	for _, email := range []string{"unknown@acme.io", ""} {
		rec := doJSON(h, http.MethodPost, "/forgot", `{"email":"`+email+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("forgot(%q) status = %d, want 200", email, rec.Code)
		}
	}
}

// TestResetFlow: register+verify a user, then /forgot -> /reset changes the
// password; the reset token is single-use (second /reset with it -> 400).
func TestResetFlow(t *testing.T) {
	users := newFakeEmailUsers()
	mailer := &fakeMailer{}
	opts := EmailOptions{Verify: true, BaseURL: "https://app.example.com"}
	h := emailChain(users, mailer, opts)

	_ = doJSON(h, http.MethodPost, "/register",
		`{"email":"user@acme.io","password":"oldpass1","tenant":"acme"}`)

	rec := doJSON(h, http.MethodPost, "/forgot", `{"email":"user@acme.io"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("forgot status = %d, want 200", rec.Code)
	}
	token := tokenFromLink(mailer.lastBdy)
	if token == "" {
		t.Fatalf("no reset token in mail: %q", mailer.lastBdy)
	}

	rec = doJSON(h, http.MethodPost, "/reset", `{"token":"`+token+`","password":"newpass2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", rec.Code)
	}
	if users.password["user@acme.io"] != "newpass2" {
		t.Fatalf("password not updated: %q", users.password["user@acme.io"])
	}
	// token is single-use
	rec = doJSON(h, http.MethodPost, "/reset", `{"token":"`+token+`","password":"again333"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reused reset token status = %d, want 400", rec.Code)
	}
}

// TestResetMissingPassword400: /reset without a password is rejected.
func TestResetMissingPassword400(t *testing.T) {
	h := emailChain(newFakeEmailUsers(), &fakeMailer{}, EmailOptions{})
	rec := doJSON(h, http.MethodPost, "/reset", `{"token":"whatever"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reset no-password status = %d, want 400", rec.Code)
	}
}

// authed installs a Principal in the request context, as SessionMiddleware would
// after authentication, so RoleMiddleware can read IsAdmin/EffectiveTenant. It
// reuses the withPrincipal helper from isolation_test.go.
func authed(r *http.Request, p Principal) *http.Request {
	return r.WithContext(withPrincipal(p))
}

func roleChain(users emailUserAPI, opts EmailOptions) server.Middleware {
	return RoleMiddleware(users, opts, nil)
}

// TestRoleRequiresAdmin403: a non-admin caller cannot change roles.
func TestRoleRequiresAdmin403(t *testing.T) {
	users := newFakeEmailUsers()
	_ = users.RegisterUserPending(context.Background(), "member@acme.io", "pw", "acme")
	h := roleChain(users, EmailOptions{})(okNext())

	req := httptest.NewRequest(http.MethodPost, "/admin/users/role",
		strings.NewReader(`{"email":"member@acme.io","admin":true}`))
	req = authed(req, Principal{Tenant: "acme", Subject: "member@acme.io", Admin: false})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin role change status = %d, want 403", rec.Code)
	}
}

// TestRolePromoteWithinTenant: a tenant admin promotes a member in their own
// tenant.
func TestRolePromoteWithinTenant(t *testing.T) {
	users := newFakeEmailUsers()
	_ = users.RegisterUserPending(context.Background(), "member@acme.io", "pw", "acme")
	h := roleChain(users, EmailOptions{PlatformTenant: "platform"})(okNext())

	req := httptest.NewRequest(http.MethodPost, "/admin/users/role",
		strings.NewReader(`{"email":"member@acme.io","admin":true}`))
	req = authed(req, Principal{Tenant: "acme", Subject: "boss@acme.io", Admin: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d, want 200", rec.Code)
	}
	if !users.admin["member@acme.io"] {
		t.Fatal("member should be admin after promote")
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["admin"] != true {
		t.Fatalf("response admin = %v, want true", m["admin"])
	}
}

// TestRoleCrossTenantDenied: a tenant admin cannot touch a user in another
// tenant (EffectiveTenant scope) -> 404 (not found in your tenant).
func TestRoleCrossTenantDenied(t *testing.T) {
	users := newFakeEmailUsers()
	_ = users.RegisterUserPending(context.Background(), "other@beta.io", "pw", "beta")
	h := roleChain(users, EmailOptions{PlatformTenant: "platform"})(okNext())

	req := httptest.NewRequest(http.MethodPost, "/admin/users/role",
		strings.NewReader(`{"email":"other@beta.io","admin":true}`))
	req = authed(req, Principal{Tenant: "acme", Subject: "boss@acme.io", Admin: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant role change status = %d, want 404", rec.Code)
	}
	if users.admin["other@beta.io"] {
		t.Fatal("cross-tenant user must NOT be modified")
	}
}

// TestPlatformAdminAnyTenant: a platform admin may change a role in any tenant.
func TestPlatformAdminAnyTenant(t *testing.T) {
	users := newFakeEmailUsers()
	_ = users.RegisterUserPending(context.Background(), "other@beta.io", "pw", "beta")
	h := roleChain(users, EmailOptions{PlatformTenant: "platform"})(okNext())

	req := httptest.NewRequest(http.MethodPost, "/admin/users/role",
		strings.NewReader(`{"email":"other@beta.io","admin":true}`))
	req = authed(req, Principal{Tenant: "platform", Subject: "root@platform", Admin: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("platform admin cross-tenant status = %d, want 200", rec.Code)
	}
	if !users.admin["other@beta.io"] {
		t.Fatal("platform admin should have promoted the user")
	}
}

// --- handler error-branch coverage (DB-free) ---

// TestRegisterVerifyDuplicate409: a second register(verify) for the same email
// is refused with 409 (the account already exists, disabled).
func TestRegisterVerifyDuplicate409(t *testing.T) {
	users := newFakeEmailUsers()
	h := emailChain(users, &fakeMailer{}, EmailOptions{Verify: true, BaseURL: "https://app.example.com"})
	body := `{"email":"dup@acme.io","password":"pw123456","tenant":"acme"}`
	if rec := doJSON(h, http.MethodPost, "/register", body); rec.Code != http.StatusAccepted {
		t.Fatalf("first register(verify) status = %d, want 202", rec.Code)
	}
	if rec := doJSON(h, http.MethodPost, "/register", body); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate register(verify) status = %d, want 409", rec.Code)
	}
}

// TestRegisterVerifyMissingFields400: an incomplete body is rejected before any
// account is created.
func TestRegisterVerifyMissingFields400(t *testing.T) {
	h := emailChain(newFakeEmailUsers(), &fakeMailer{}, EmailOptions{Verify: true})
	for _, body := range []string{
		`{"password":"pw123456","tenant":"acme"}`,       // no email
		`{"email":"x@acme.io","tenant":"acme"}`,         // no password
		`{"email":"x@acme.io","password":"pw123456"}`,   // no tenant
	} {
		if rec := doJSON(h, http.MethodPost, "/register", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("register(verify) %q status = %d, want 400", body, rec.Code)
		}
	}
}

// TestEmailFlowsMethodNotAllowed405: each public flow rejects the wrong method.
func TestEmailFlowsMethodNotAllowed405(t *testing.T) {
	h := emailChain(newFakeEmailUsers(), &fakeMailer{}, EmailOptions{Verify: true})
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/register"},  // register is POST-only
		{http.MethodPost, "/verify"},   // verify is GET-only
		{http.MethodGet, "/forgot"},    // forgot is POST-only
		{http.MethodGet, "/reset"},     // reset is POST-only
	}
	for _, c := range cases {
		rec := doJSON(h, c.method, c.path, "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want 405", c.method, c.path, rec.Code)
		}
	}
}

// TestEmailFlowsBadJSON400: malformed JSON bodies are rejected with 400.
func TestEmailFlowsBadJSON400(t *testing.T) {
	h := emailChain(newFakeEmailUsers(), &fakeMailer{}, EmailOptions{Verify: true})
	for _, path := range []string{"/register", "/forgot", "/reset"} {
		rec := doJSON(h, http.MethodPost, path, `{not json`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s bad-json status = %d, want 400", path, rec.Code)
		}
	}
}

// TestResetUnknownUser400: a syntactically valid reset token that maps to a
// non-existent user is reported as an invalid token (400), not a distinct error
// (no probe signal for which emails exist).
func TestResetUnknownUser400(t *testing.T) {
	users := newFakeEmailUsers()
	// Mint a reset token for an email that was never registered.
	tok, _ := users.IssueToken(context.Background(), "ghost@acme.io", PurposeReset)
	h := emailChain(users, &fakeMailer{}, EmailOptions{})
	rec := doJSON(h, http.MethodPost, "/reset", `{"token":"`+tok+`","password":"newpass2"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reset unknown-user status = %d, want 400", rec.Code)
	}
}

// TestRoleUnknownUser404: promoting an email that does not exist yields 404.
func TestRoleUnknownUser404(t *testing.T) {
	h := roleChain(newFakeEmailUsers(), EmailOptions{PlatformTenant: "platform"})(okNext())
	req := httptest.NewRequest(http.MethodPost, "/admin/users/role",
		strings.NewReader(`{"email":"nobody@acme.io","admin":true}`))
	req = authed(req, Principal{Tenant: "platform", Subject: "root@platform", Admin: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("role unknown-user status = %d, want 404", rec.Code)
	}
}

// TestRoleBadRequest400: admin caller, but a body missing email is a 400.
func TestRoleBadRequest400(t *testing.T) {
	h := roleChain(newFakeEmailUsers(), EmailOptions{PlatformTenant: "platform"})(okNext())
	req := httptest.NewRequest(http.MethodPost, "/admin/users/role", strings.NewReader(`{"admin":true}`))
	req = authed(req, Principal{Tenant: "platform", Subject: "root@platform", Admin: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("role missing-email status = %d, want 400", rec.Code)
	}
}

// TestNonOwnedPathPassThrough: a path neither middleware owns falls through to
// next (okNext -> 200), proving they don't over-capture.
func TestNonOwnedPathPassThrough(t *testing.T) {
	eh := emailChain(newFakeEmailUsers(), &fakeMailer{}, EmailOptions{Verify: true})
	if rec := doJSON(eh, http.MethodGet, "/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("EmailMiddleware /health status = %d, want 200 (passthrough)", rec.Code)
	}
	rh := roleChain(newFakeEmailUsers(), EmailOptions{})(okNext())
	if rec := doJSON(rh, http.MethodGet, "/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("RoleMiddleware /health status = %d, want 200 (passthrough)", rec.Code)
	}
}
