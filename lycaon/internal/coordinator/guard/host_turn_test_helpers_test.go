package guard_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
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

func observeHasCode(t *testing.T, gc *oar.GuardContext, code string) bool {
	return oartest.EvaluateHasCode(t, gc, oar.AnchorCoordinatorPreInvoke, code) ||
		oartest.EvaluateHasCode(t, gc, oar.AnchorToolPreInvoke, code) ||
		oartest.EvaluateHasCode(t, gc, oar.AnchorCoordinatorCloseoutCheck, code)
}
