package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestRegistryPublishesOneDefinitionSnapshot(t *testing.T) {
	reg := NewDefaultRegistry()
	handler := func(context.Context, map[string]any, ToolContext) (string, error) { return "ok", nil }
	testutil.FailErr(t, "register read", reg.RegisterDerived(ToolMeta{
		Name: "read", Description: "Read a file", ArgsSchema: map[string]any{"type": "object"},
	}, handler))
	def, ok := reg.Definition("read")
	if !ok || def.Handler == nil || def.Meta.Description != "Read a file" || def.Contract.Owner != "filesystem" {
		t.Fatalf("definition = %+v, ok=%v", def, ok)
	}
}

func TestRegistryPrefixLeaseMakesSourceStateAndDefinitionsAtomic(t *testing.T) {
	reg := NewDefaultRegistry()
	handler := func(context.Context, map[string]any, ToolContext) (string, error) { return "ok", nil }
	registerTestDefinition(t, reg, "mcp_old_read", handler)
	release := reg.LeasePrefix("mcp_old_read", "mcp_")
	started := make(chan struct{})
	callbackEntered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- reg.ReplacePrefix("mcp_", []Definition{{
			Meta:     ToolMeta{Name: "mcp_new_read", ArgsSchema: map[string]any{"type": "object"}},
			Contract: toolcontract.External("mcp:new"), Handler: handler,
		}}, func() error {
			close(callbackEntered)
			return nil
		})
	}()
	<-started
	select {
	case <-callbackEntered:
		t.Fatal("definition transaction crossed a live invocation lease")
	case <-time.After(50 * time.Millisecond):
	}
	if _, ok := reg.Definition("mcp_old_read"); !ok {
		t.Fatal("leased definition changed before invocation release")
	}
	release()
	select {
	case err := <-done:
		testutil.FailErr(t, "replace after invocation release", err)
	case <-time.After(5 * time.Second):
		t.Fatal("definition transaction did not resume after release")
	}
	if _, ok := reg.Definition("mcp_new_read"); !ok {
		t.Fatal("new generation missing after atomic publication")
	}
}

func TestRegistryPrefixPublicationFailureKeepsPriorGeneration(t *testing.T) {
	reg := NewDefaultRegistry()
	handler := func(context.Context, map[string]any, ToolContext) (string, error) { return "ok", nil }
	registerTestDefinition(t, reg, "mcp_old_read", handler)
	wantErr := errors.New("source state unavailable")
	err := reg.ReplacePrefix("mcp_", []Definition{{
		Meta:     ToolMeta{Name: "mcp_new_read", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("mcp:new"), Handler: handler,
	}}, func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("transaction error = %v, want %v", err, wantErr)
	}
	if _, ok := reg.Definition("mcp_old_read"); !ok {
		t.Fatal("failed source-state transaction removed prior generation")
	}
	if _, ok := reg.Definition("mcp_new_read"); ok {
		t.Fatal("failed source-state transaction published new generation")
	}
}

func TestRegistryRejectsUndeclaredTool(t *testing.T) {
	reg := NewDefaultRegistry()
	err := reg.Register("undeclared", func(context.Context, map[string]any, ToolContext) (string, error) {
		return "", nil
	})
	if err == nil {
		t.Fatal("expected missing contract error")
	}
	if _, ok := reg.Definition("undeclared"); ok {
		t.Fatal("undeclared tool was published")
	}
}

func TestRegistryMarksTheOwnerBoundary(t *testing.T) {
	reg := NewDefaultRegistry()
	testutil.FailErr(t, "register read", reg.Register("read", func(context.Context, map[string]any, ToolContext) (string, error) {
		return "ok", nil
	}))
	out := &ToolInvocationOut{}
	_, err := reg.Run(t.Context(), "read", nil, ToolContext{
		Effects: InvocationEffects{Out: out},
	})
	testutil.FailErr(t, "run read", err)
	if !out.OwnerInvoked {
		t.Fatal("subsystem-owner boundary was not marked")
	}
}

func TestRegistryRejectsNonCanonicalDefinitions(t *testing.T) {
	reg := NewDefaultRegistry()
	err := reg.RegisterDefinition(Definition{
		Meta: ToolMeta{Name: "Read", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.Contract{
			Owner: "filesystem", Lifecycle: toolcontract.LifecycleReadOnly,
		},
		Handler: func(context.Context, map[string]any, ToolContext) (string, error) { return "", nil },
	})
	if err == nil {
		t.Fatal("non-canonical definition was published")
	}
}

func TestRegistryReplacesDynamicDefinitionsAtomically(t *testing.T) {
	reg := NewDefaultRegistry()
	handler := func(context.Context, map[string]any, ToolContext) (string, error) { return "ok", nil }
	registerTestDefinition(t, reg, "mcp_old_read", handler)
	testutil.FailErr(t, "register native read", reg.Register("read", handler))
	replacement := Definition{
		Meta:     ToolMeta{Name: "mcp_new_read", ArgsSchema: map[string]any{"type": "object"}},
		Contract: toolcontract.External("mcp:new"), Handler: handler,
	}
	testutil.FailErr(t, "replace mcp definitions", reg.ReplacePrefix("mcp_", []Definition{replacement}, nil))
	if _, ok := reg.Definition("mcp_old_read"); ok {
		t.Fatal("old dynamic definition remains")
	}
	if _, ok := reg.Definition("mcp_new_read"); !ok {
		t.Fatal("new dynamic definition is missing")
	}
	if _, ok := reg.Definition("read"); !ok {
		t.Fatal("native definition was replaced")
	}

	bad := replacement
	bad.Meta.Name = "outside"
	if err := reg.ReplacePrefix("mcp_", []Definition{bad}, nil); err == nil {
		t.Fatal("out-of-prefix replacement succeeded")
	}
	if _, ok := reg.Definition("mcp_new_read"); !ok {
		t.Fatal("failed replacement changed the registry")
	}
}

func TestCatalogRegistryPublishesDeclaredMetadata(t *testing.T) {
	catalog := &toolschema.Config{Tools: map[string]toolschema.Entry{
		"read": {Description: "Read", Schema: map[string]any{"type": "object"}},
	}}
	reg, err := NewCatalogRegistry(catalog)
	testutil.FailErr(t, "create registry", err)
	catalog.Tools["read"] = toolschema.Entry{Description: "Changed", Schema: map[string]any{"type": "string"}}
	handler := func(context.Context, map[string]any, ToolContext) (string, error) { return "ok", nil }
	testutil.FailErr(t, "register read", reg.Register("read", handler))
	read, _ := reg.Definition("read")
	if read.Meta.Description != "Read" {
		t.Fatalf("description = %q", read.Meta.Description)
	}
	read.Meta.ArgsSchema["type"] = "string"
	read, _ = reg.Definition("read")
	if read.Meta.ArgsSchema["type"] != "object" {
		t.Fatalf("schema changed through snapshot: %+v", read.Meta.ArgsSchema)
	}
	if err := reg.RegisterDerived(ToolMeta{Name: "read", Description: "inline"}, handler); err == nil {
		t.Fatal("catalog metadata was overridden")
	}

	if err := reg.Register("list_dir", handler); err == nil {
		t.Fatal("tool without metadata was published")
	}
	if _, ok := reg.Definition("list_dir"); ok {
		t.Fatal("undeclared metadata tool was published")
	}
}

func TestToolMetaIsMCP(t *testing.T) {
	if !(ToolMeta{Source: ToolSourceMCP}).IsMCP() {
		t.Fatal("Source=mcp must be MCP")
	}
	if !(ToolMeta{Name: "mcp_coropa_intel_search"}).IsMCP() {
		t.Fatal("mcp_ prefix must be MCP")
	}
	if (ToolMeta{Name: "read"}).IsMCP() {
		t.Fatal("native tools are not MCP")
	}
}
