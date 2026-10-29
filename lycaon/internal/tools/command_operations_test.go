package tools

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandOperationIsBoundButNeverCallable(t *testing.T) {
	registry := NewDefaultRegistry()
	testutil.FailErr(t, "register read operation", registry.RegisterOperation("read"))
	operation, ok := registry.Operation("read")
	if !ok || operation.Handler != nil || operation.Contract.Owner != "filesystem" || operation.Operation.Digest() == "" {
		t.Fatalf("operation=%+v found=%v", operation, ok)
	}
	if _, offered := registry.Definition("read"); offered || len(registry.List()) != 0 {
		t.Fatal("command operation became a callable tool")
	}
	if _, err := registry.Run(t.Context(), "read", nil, ToolContext{}); err == nil {
		t.Fatal("command operation ran through the tool interface")
	}
	operation.Meta.ArgsSchema["mutated"] = true
	again, _ := registry.Operation("read")
	if again.Meta.ArgsSchema["mutated"] != nil {
		t.Fatal("operation metadata leaked mutable state")
	}
	if _, ok := registry.Meta("read"); !ok {
		t.Fatal("retained operation metadata is unavailable")
	}
	if err := registry.Register("read", func(context.Context, map[string]any, ToolContext) (string, error) { return "", nil }); err == nil {
		t.Fatal("callable tool replaced a command operation")
	}
}

func TestCommandOperationsRejectDomainToolsAndCallableCollisions(t *testing.T) {
	registry := NewDefaultRegistry()
	if err := registry.RegisterOperation("summarize"); err == nil {
		t.Fatal("domain tool classified as a command-only operation")
	}
	testutil.FailErr(t, "register read fixture", registry.Register("read", func(context.Context, map[string]any, ToolContext) (string, error) { return "", nil }))
	if err := registry.RegisterOperation("read"); err == nil {
		t.Fatal("command operation replaced a callable tool")
	}
}
