package toolexecution

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"path/filepath"
	"testing"
)

func TestPathDenialsPreserveUserDirection(t *testing.T) {
	decline := func(context.Context, string, map[string]any, tools.ToolContext, string) (bool, bool, string, error) {
		return false, true, "Keep the existing location.", nil
	}
	e := func() *Executor {
		e := NewExecutor(nil, nil, "")
		e.Boundary.readPathPreflight = decline
		e.Boundary.writeRootPreflight = decline
		return e
	}()
	tc := tools.ToolContext{
		Invocation: tools.Invocation{Contract: toolcontract.Contract{Capabilities: toolcontract.CapabilityReadPath | toolcontract.CapabilityWriteRoot}},
	}
	for _, field := range []string{"read_path", "write_root"} {
		path := filepath.Join(t.TempDir(), "intended")
		args := map[string]any{"capability_request": map[string]any{field: path}}
		var err error
		if field == "read_path" {
			err = e.Boundary.preflightReadPath(t.Context(), "command", args, &tc)
		} else {
			err = e.Boundary.preflightWriteRoot(t.Context(), "command", args, &tc)
		}
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Data[toolrejection.UserGuidanceKey] != "Keep the existing location." || reject.Data["path"] != path {
			t.Fatalf("denial lost direction or path: %v", err)
		}
	}
}
