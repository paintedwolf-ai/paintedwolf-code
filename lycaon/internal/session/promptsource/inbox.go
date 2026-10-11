package promptsource

import (
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	"github.com/lycaon/lycaon/internal/session/submissions"
)

type Inbox struct {
	Guidance    *guidancedelivery.Service
	Submissions *submissions.Service
}

func (m *Inbox) Build() promptloop.InboxDeps {
	deps := promptloop.InboxDeps{
		TakeUserSend:       m.Submissions.TakeSend,
		TakePolicyFeedback: m.Guidance.TakePolicy,
		TakePhaseGuidance:  m.Guidance.TakePhase,
	}

	return deps
}
