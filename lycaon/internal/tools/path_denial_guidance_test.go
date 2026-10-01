package tools

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"path/filepath"
	"testing"
)

func TestPathDenialsPreserveUserDirection(t *testing.T) {
	decline := func(context.Context, string, map[string]any, ToolContext, string) (bool, bool, string, error) {
		return false, true, "Keep the existing location.", nil
	}
	e := &DefaultToolExecutor{readPathPreflight: decline, writeRootPreflight: decline}
	tc := ToolContext{Invocation: Invocation{Contract: toolcontract.Contract{Capabilities: toolcontract.CapabilityReadPath | toolcontract.CapabilityWriteRoot}}}
	for _, field := range []string{"read_path", "write_root"} {
		path := filepath.Join(t.TempDir(), "intended")
		args := map[string]any{"capability_request": map[string]any{field: path}}
		var err error
		if field == "read_path" {
			err = e.preflightReadPath(t.Context(), "command", args, &tc)
		} else {
			err = e.preflightWriteRoot(t.Context(), "command", args, &tc)
		}
		var reject *ToolReject
		if !errors.As(err, &reject) || reject.Data[UserGuidanceKey] != "Keep the existing location." || reject.Data["path"] != path {
			t.Fatalf("denial lost direction or path: %v", err)
		}
	}
}
