package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestCommandOperationDiscoveryRejectsBeforeResolution(t *testing.T) {
	reg := NewDefaultRegistry()
	testutil.FailErr(t, "register operation", reg.RegisterOperation("edit"))
	request := requestTools{reg: reg, deps: RequestToolsDeps{Resolve: func(context.Context, ToolContext, string, []turnload.ToolCard) turnload.RequestOutcome {
		t.Fatal("retired operation reached decision resolution")
		return turnload.RequestOutcome{}
	}}}
	_, err := request.run(t.Context(), map[string]any{"need": "edit"}, ToolContext{})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_COMMAND_OPERATION" || reject.Data["need"] != "edit" {
		t.Fatalf("operation rejection = %v", err)
	}
	for _, need := range []string{"edit a file", "command", "mcp_edit"} {
		if got := CommandOperationReject(need); got != nil {
			t.Errorf("non-operation %q rejected: %v", need, got)
		}
	}
}

func TestCatalogRegistryCannotPublishCommandOperationHandler(t *testing.T) {
	reg, err := NewCatalogRegistry(&toolschema.Config{Tools: map[string]toolschema.Entry{"edit": {Description: "Edit", Schema: map[string]any{"type": "object"}}}})
	testutil.FailErr(t, "create registry", err)
	handler := func(context.Context, map[string]any, ToolContext) (string, error) { return "", nil }
	if err := reg.Register("edit", handler); err == nil {
		t.Fatal("command operation registered as tool")
	}
	contract, _ := toolcontract.Lookup("edit")
	if err := reg.RegisterDefinition(Definition{Meta: ToolMeta{Name: "edit"}, Contract: contract, Handler: handler}); err == nil {
		t.Fatal("command operation definition registered")
	}
	if err := reg.ReplacePrefix("edit", []Definition{{Meta: ToolMeta{Name: "edit"}, Contract: contract, Handler: handler}}, nil); err == nil {
		t.Fatal("command operation published through prefix")
	}
	testutil.FailErr(t, "register operation", reg.RegisterOperation("edit"))
	if _, ok := reg.Definition("edit"); ok {
		t.Fatal("operation exposed as callable definition")
	}
	if _, ok := reg.Operation("edit"); !ok {
		t.Fatal("operation contract missing")
	}
}
