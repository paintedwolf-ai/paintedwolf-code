package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// PendingUserInputTaskForbiddenCode rejects task() while a coordinator→human wait is open.
const PendingUserInputTaskForbiddenCode = "PENDING_USER_INPUT_TASK_FORBIDDEN"

// ObserveTaskWhilePendingUserInput publishes pending_user_input for task().
func ObserveTaskWhilePendingUserInput(
	sess *api.Session,
	implState surface.ImplementSessionState,
	toolName string,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil {
		return
	}
	gc.Tool = strings.TrimSpace(strings.ToLower(toolName))
	gc.DeriveToolClassFacts()
	if gc.Tool != "task" || strings.TrimSpace(sess.ParentSessionID) != "" {
		return
	}
	gc.PendingUserInput = implState.PendingUserInput
}
