package workeradmission

import (
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RequireAskReadScope checks the profile and the resolved work scope together.
func RequireAskReadScope(tool string, mutationCapable, known bool, scope api.TaskScope) error {
	scope = scope.Normalized()
	if !known || mutationCapable || scope.Mode != api.TaskScopeModeRead || scope.BaseOverlayID != "" {
		return tools.AskModeReject(tool)
	}
	return nil
}
