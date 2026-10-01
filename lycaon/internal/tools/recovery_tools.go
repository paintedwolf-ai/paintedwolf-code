package tools

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/oar"
)

type recoveryToolsKey struct{}

// WithRecoveryTools freezes the tool names actually offered on this request.
// An absent list provides no evidence that a proposed recovery tool is available.
func WithRecoveryTools(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, recoveryToolsKey{}, slices.Clone(names))
}

// ToolOffered reports membership in the request's frozen schema list.
func ToolOffered(ctx context.Context, name string) bool {
	names, _ := ctx.Value(recoveryToolsKey{}).([]string)
	return slices.Contains(names, name)
}

// RegisterRecoveryFacts publishes the frozen offered-tool snapshot lazily.
func RegisterRecoveryFacts(ctx context.Context, gc *oar.GuardContext) {
	names, _ := ctx.Value(recoveryToolsKey{}).([]string)
	for _, tool := range []string{"read", "list_dir", "find", "grep", "request_tools", "task", "complete_leg", "terminal_open", "terminal_close", "terminal_read", "command_stop", "command_output", "secret_generate", "ask_user", "capture_page", "measure_page", "page_act", "page_snapshot", "page_close", "stat", "command"} {
		fact := "can_" + tool
		gc.RegisterProvider("paintedwolf."+fact, func(target *oar.GuardContext) error {
			if target.ObservationData == nil {
				target.ObservationData = map[string]any{}
			}
			target.ObservationData[fact] = slices.Contains(names, tool)
			return nil
		})
	}
}
