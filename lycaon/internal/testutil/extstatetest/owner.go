// Package extstatetest wires extension subsystem owners for tests.
package extstatetest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
)

// Owner builds a quiet subsystem owner.
func Owner(t *testing.T) *extensionstate.Owner {
	t.Helper()
	root := configlayout.FindModuleRoot()
	if root == "" {
		t.Fatal("module root not found")
	}
	log := slog.New(slog.DiscardHandler)
	return &extensionstate.Owner{
		Views: catalogview.NewCache(root, log),
	}
}

// Apply submits one intent against the current state revision.
func Apply(t *testing.T, owner *extensionstate.Owner, scope extensionstate.Scope, op extensionstate.Op) extensionstate.Result {
	t.Helper()
	result, err := TryApply(t, owner, scope, op)
	if err != nil {
		t.Fatalf("extension mutation %T: %v", op, err)
	}
	return result
}

// TryApply submits one intent and returns its error for rejection tests.
func TryApply(t *testing.T, owner *extensionstate.Owner, scope extensionstate.Scope, op extensionstate.Op) (extensionstate.Result, error) {
	t.Helper()
	dir := ""
	if scope.Kind == "project" {
		dir = scope.ProjectDir
	}
	revision, err := owner.CurrentRevision(dir)
	if err != nil {
		t.Fatalf("current extension revision: %v", err)
	}
	return owner.Apply(context.Background(), extensionstate.Intent{
		Scope:            scope,
		ExpectedRevision: revision,
		Op:               op,
	})
}

// InstallPack installs one source at the given scope for fixture setup.
func InstallPack(t *testing.T, scope extensionstate.Scope, source, version, ref string) extensionstate.Result {
	t.Helper()
	return Apply(t, Owner(t), scope, extensionstate.InstallOp{Source: source, Version: version, Ref: ref})
}

// DeviceScope is the device mutation target.
func DeviceScope() extensionstate.Scope { return extensionstate.Scope{Kind: "device"} }
