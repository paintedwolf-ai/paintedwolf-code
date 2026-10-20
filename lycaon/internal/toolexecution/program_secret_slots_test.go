package toolexecution

import (
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"testing"
)

func TestNativeSecretReferencesRequireExplicitValueSinks(t *testing.T) {
	reference := secretmatch.ReferenceToken("123e4567-e89b-42d3-a456-426614174000")
	contract := toolcontract.Contract{SecretReferenceSurface: toolcontract.SecretSurfaceFile, SecretReferenceArgs: []string{"content"}}
	for _, key := range []string{"executable", "script", "destination", "path"} {
		if err := validateNativeSecretSlots(contract, map[string]any{key: reference}); err == nil {
			t.Fatalf("reference entered undeclared %s sink", key)
		}
	}
	if err := validateNativeSecretSlots(contract, map[string]any{"content": reference}); err != nil {
		t.Fatalf("declared content sink rejected: %v", err)
	}
	if err := validateNativeSecretSlots(contract, map[string]any{"script": "{{paintedwolf-secret:broken}}"}); err == nil {
		t.Fatal("malformed undeclared reference accepted")
	}
	contract.SecretReferenceArgs = nil
	if err := validateNativeSecretSlots(contract, map[string]any{"content": reference}); err == nil {
		t.Fatal("native nil slots inherited structured whole-tree authority")
	}
	if err := validateNativeSecretSlots(contract, map[string]any{"script": reference, "resolve_secret_references": false}); err != nil {
		t.Fatalf("explicit literal reference rejected: %v", err)
	}
	contract.SecretReferenceSurface = toolcontract.SecretSurfaceNone
	if err := validateNativeSecretSlots(contract, map[string]any{"script": reference}); err != nil {
		t.Fatalf("nonresolving operation lost literal behavior: %v", err)
	}
}
