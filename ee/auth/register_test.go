// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postRegister drives /register through the middleware and returns the recorder.
func postRegister(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestRegisterDisabled404: with allowRegister=false the route is invisible.
func TestRegisterDisabled404(t *testing.T) {
	h := SessionMiddleware(newFakeUsers(), newFakeSessions(), denyKeys{}, false, nil)(okNext())
	rec := postRegister(h, `{"email":"new@acme.io","password":"pw123456","tenant":"acme"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled /register status = %d, want 404", rec.Code)
	}
}

// TestRegisterMissingFields400: enabled but incomplete body -> 400.
func TestRegisterMissingFields400(t *testing.T) {
	h := SessionMiddleware(newFakeUsers(), newFakeSessions(), denyKeys{}, true, nil)(okNext())
	for _, body := range []string{
		`{"password":"pw123456","tenant":"acme"}`,       // no email
		`{"email":"new@acme.io","tenant":"acme"}`,       // no password
		`{"email":"new@acme.io","password":"pw123456"}`, // no tenant
	} {
		rec := postRegister(h, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("register %q status = %d, want 400", body, rec.Code)
		}
	}
}

// TestRegisterSuccessNeverAdmin: a valid signup returns 201 and the stored user
// is an ordinary member (admin=false), never elevated.
func TestRegisterSuccessNeverAdmin(t *testing.T) {
	users := newFakeUsers()
	h := SessionMiddleware(users, newFakeSessions(), denyKeys{}, true, nil)(okNext())

	rec := postRegister(h, `{"email":"New@Acme.io","password":"pw123456","tenant":"acme"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", rec.Code)
	}
	// Email is normalized on the way in.
	stored, ok := users.registered["new@acme.io"]
	if !ok {
		t.Fatal("registered user not recorded (email not normalized?)")
	}
	if stored.Admin {
		t.Fatal("self-registered user must NOT be admin")
	}
	if stored.Tenant != "acme" {
		t.Fatalf("stored tenant = %q, want acme", stored.Tenant)
	}
}

// TestRegisterDuplicate409: registering an existing email is refused with 409,
// so it can't overwrite another account.
func TestRegisterDuplicate409(t *testing.T) {
	users := newFakeUsers()
	h := SessionMiddleware(users, newFakeSessions(), denyKeys{}, true, nil)(okNext())

	body := `{"email":"dup@acme.io","password":"pw123456","tenant":"acme"}`
	if rec := postRegister(h, body); rec.Code != http.StatusCreated {
		t.Fatalf("first register status = %d, want 201", rec.Code)
	}
	if rec := postRegister(h, body); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, want 409", rec.Code)
	}
}
