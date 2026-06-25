package core

import (
	"context"
	"testing"
)

func newCtx() *AIGisContext {
	return NewGatewayContext(context.Background(), nil)
}

func TestVaultStoreGet(t *testing.T) {
	ctx := newCtx()
	ctx.VaultStore("__AIGIS_SEC_aaaaaaaaaaaa__", "secret@x.com")

	if v, ok := ctx.VaultGet("__AIGIS_SEC_aaaaaaaaaaaa__"); !ok || v != "secret@x.com" {
		t.Errorf("VaultGet = (%q,%v), want (secret@x.com,true)", v, ok)
	}
	if _, ok := ctx.VaultGet("__AIGIS_SEC_missing0000__"); ok {
		t.Error("VaultGet of missing key should return ok=false")
	}
	if all := ctx.VaultGetAll(); len(all) != 1 {
		t.Errorf("VaultGetAll len = %d, want 1", len(all))
	}
}

func TestDetections(t *testing.T) {
	ctx := newCtx()
	ctx.RecordDetection("Email", "__AIGIS_SEC_aaaaaaaaaaaa__", "te***om")
	ctx.RecordDetection("Mobile Phone", "__AIGIS_SEC_bbbbbbbbbbbb__", "13***00")

	d := ctx.Detections()
	if len(d) != 2 {
		t.Fatalf("Detections len = %d, want 2", len(d))
	}
	if d[0].Type != "Email" || d[0].Preview != "te***om" {
		t.Errorf("Detections[0] = %+v", d[0])
	}

	// Detections returns a copy: mutating it must not affect the context.
	d[0].Type = "mutated"
	if again := ctx.Detections(); again[0].Type != "Email" {
		t.Error("Detections() should return a copy, not the backing slice")
	}
}

func TestMetadataCopySemantics(t *testing.T) {
	ctx := newCtx()
	ctx.SetMetadata("model", "glm-4.6")

	if v, ok := ctx.GetMetadata("model"); !ok || v != "glm-4.6" {
		t.Errorf("GetMetadata = (%v,%v), want (glm-4.6,true)", v, ok)
	}

	// Metadata() is a copy; mutating it must not leak back.
	m := ctx.Metadata()
	m["model"] = "changed"
	if v, _ := ctx.GetMetadata("model"); v != "glm-4.6" {
		t.Errorf("Metadata() should be a copy; ctx value changed to %v", v)
	}
}
