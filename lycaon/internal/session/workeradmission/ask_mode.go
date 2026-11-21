package workeradmission

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RequireAskReadScope checks the profile and the resolved work scope together.
func RequireAskReadScope(tool string, mutationCapable, known bool, scope api.TaskScope) error {
	scope = scope.Normalized()
	if !known || mutationCapable {
		return &toolrejection.ToolReject{
			Code: tools.AskModeActiveCode,
			Data: map[string]any{"tool": tool, "reason": "mutating_profile", "profile_mutation_capable": true, "scope_mode": string(scope.Mode)},
		}
	}
	if scope.Mode != api.TaskScopeModeRead || scope.BaseOverlayID != "" {
		return &toolrejection.ToolReject{
			Code: tools.AskModeActiveCode,
			Data: map[string]any{"tool": tool, "reason": "write_scope", "profile_mutation_capable": false, "scope_mode": string(scope.Mode)},
		}
	}
	return nil
}
