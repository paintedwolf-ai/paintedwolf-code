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
		return toolrejection.NewAskWorkerScopeReject(tools.AskModeActiveCode, tool, "mutating_profile", true, scope.Mode)
	}
	if scope.Mode != api.TaskScopeModeRead || scope.BaseOverlayID != "" {
		return toolrejection.NewAskWorkerScopeReject(tools.AskModeActiveCode, tool, "write_scope", false, scope.Mode)
	}
	return nil
}
