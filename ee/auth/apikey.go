// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"errors"
	"net/http"
)

// ErrUnauthorized is returned when a request carries no valid credential.
var ErrUnauthorized = errors.New("missing or invalid API key")

// StaticAPIKeyProvider authenticates via a fixed API-key -> tenant map. It is a
// minimal, dependency-free AuthProvider suitable as a starting point; a real
// deployment would back this with a database, OIDC, or a secrets store.
type StaticAPIKeyProvider struct {
	// keys maps an API key (the Bearer token) to its tenant identifier.
	keys map[string]string
}

// NewStaticAPIKeyProvider builds a provider from an apiKey -> tenant map.
func NewStaticAPIKeyProvider(keys map[string]string) *StaticAPIKeyProvider {
	return &StaticAPIKeyProvider{keys: keys}
}

// Authenticate resolves the request's Bearer token to a tenant. Unknown or
// missing keys are rejected.
func (p *StaticAPIKeyProvider) Authenticate(r *http.Request) (Principal, error) {
	token := bearerToken(r)
	if token == "" {
		return Principal{}, ErrUnauthorized
	}
	tenant, ok := p.keys[token]
	if !ok {
		return Principal{}, ErrUnauthorized
	}
	return Principal{Tenant: tenant, Subject: token}, nil
}
