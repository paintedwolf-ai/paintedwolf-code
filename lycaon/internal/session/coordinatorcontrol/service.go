package coordinatorcontrol

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/closeoutassembly"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/policyindex"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/session/promptsource"
	"github.com/lycaon/lycaon/internal/session/turnadmission"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/turnnudges"
)

type Service struct {
	Admission       *turnadmission.Service
	Runtime         *coordinator.Runtime
	Workers         *Workers
	Scans           *Scans
	Context         *promptsource.Context
	Model           *promptsource.Model
	Tools           *promptsource.Tools
	Control         *promptsource.Control
	Projection      *promptsource.Projection
	Nudging         *promptsource.Nudges
	Inbox           *promptsource.Inbox
	Completion      *promptsource.Closeout
	Assembly        *promptsource.Assembly
	Loop            *promptsource.Loop
	Guards          *turnguards.Service
	Guidance        *guidancedelivery.Service
	Loading         *loading.Service
	Profiles        *profiles.Service
	Batch           *batchcontrol.Service
	Nudges          *turnnudges.Service
	Closeout        *closeoutassembly.Service
	ProgressClosure *progressclosure.Service
	PolicyIndex     *policyindex.Service
	ToolPolicy      *policyfacts.Service
	Feedback        *policyfeedback.Service
}

func (m *Service) WaitForTurns(ctx context.Context) {
	if m == nil {
		return
	}
	m.Runtime.CoordinatorLoop().WaitForAsyncTurns(ctx)
	m.Admission.WaitDrains(ctx)
}
