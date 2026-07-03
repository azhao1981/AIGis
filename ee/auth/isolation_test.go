// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"testing"
)

// withPrincipal returns a context carrying p, mirroring what the auth middleware
// stashes so the isolation helpers can be tested without HTTP plumbing.
func withPrincipal(p Principal) context.Context {
	return context.WithValue(context.Background(), tenantKey{}, p)
}

// TestEffectiveTenantPlatformAdmin: an admin belonging to the platform tenant is
// unscoped (sees every tenant).
func TestEffectiveTenantPlatformAdmin(t *testing.T) {
	ctx := withPrincipal(Principal{Tenant: "ops", Subject: "root", Admin: true})
	scope, isPlatform := EffectiveTenant(ctx, "ops")
	if !isPlatform {
		t.Fatal("platform-tenant admin should be a platform admin")
	}
	if scope != "" {
		t.Fatalf("platform admin scope = %q, want \"\" (no filter)", scope)
	}
}

// TestEffectiveTenantTenantAdmin: an admin of any other tenant is pinned to their
// own tenant regardless of the requested filter.
func TestEffectiveTenantTenantAdmin(t *testing.T) {
	ctx := withPrincipal(Principal{Tenant: "acme", Subject: "alice", Admin: true})
	scope, isPlatform := EffectiveTenant(ctx, "ops")
	if isPlatform {
		t.Fatal("non-platform-tenant admin must not be a platform admin")
	}
	if scope != "acme" {
		t.Fatalf("tenant admin scope = %q, want \"acme\"", scope)
	}
}

// TestEffectiveTenantNoPrincipal: with no resolved principal (guarded elsewhere
// by IsAdmin), the helper is a safe non-platform, empty scope.
func TestEffectiveTenantNoPrincipal(t *testing.T) {
	scope, isPlatform := EffectiveTenant(context.Background(), "ops")
	if isPlatform || scope != "" {
		t.Fatalf("no principal = (%q,%v), want (\"\",false)", scope, isPlatform)
	}
}

// TestEffectiveTenantConfigurablePlatform: the platform tenant name is
// configurable, so a deployment can use something other than "ops".
func TestEffectiveTenantConfigurablePlatform(t *testing.T) {
	ctx := withPrincipal(Principal{Tenant: "hq", Subject: "root", Admin: true})
	if _, isPlatform := EffectiveTenant(ctx, "hq"); !isPlatform {
		t.Fatal("admin of the configured platform tenant should be platform-wide")
	}
	if _, isPlatform := EffectiveTenant(ctx, "ops"); isPlatform {
		t.Fatal("admin of \"hq\" must not be platform when platform tenant is \"ops\"")
	}
}
