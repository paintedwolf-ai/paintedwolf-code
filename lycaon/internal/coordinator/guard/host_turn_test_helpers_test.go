package guard_test

import (
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func hostLoopHistory(history []api.Message) []api.Message {
	return append(history, api.Message{
		Role:       api.MessageRoleUser,
		Origin:     api.MessageOriginHost,
		Visibility: api.MessageVisibilityInternal,
		Kind:       api.MessageKindHostLoopWake,
	})
}

func observeHasCode(gc *oar.GuardContext, code string) bool {
	return guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorPreInvoke, code) ||
		guard.EvaluateObserveHasCode(gc, oar.AnchorToolPreInvoke, code) ||
		guard.EvaluateObserveHasCode(gc, oar.AnchorCoordinatorCloseoutCheck, code)
}
