package terminal

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestSendRefusesUnsupportedObservationBeforeWritingTerminal(t *testing.T) {
	for _, observe := range []any{true, "unsupported"} {
		out, err := SendHandler(nil)(t.Context(), map[string]any{"id": "terminal", "input": "must not write", "observe": observe}, tools.ToolContext{})
		var rejection *toolrejection.ToolReject
		if out != "" || !errors.As(err, &rejection) || rejection.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("out=%q error=%v", out, err)
		}
	}
}
