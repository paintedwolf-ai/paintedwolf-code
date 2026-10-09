package sessions

import (
	"context"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/spawn"
)

// ResourceTracker tracks closers registered during runtime wiring.
type ResourceTracker interface {
	Track(name string, priority int, closeFn func(context.Context) error)
}

// Runtime holds the wired session manager, checkpoints, runtimes, and prompt engines.
type Runtime struct {
	Manager             *session.Host
	Store               session.Store
	PromptEngine        *prompts.FileTemplateEngine
	Invocations         invocation.Recorder
	Checkpoints         hitl.CheckpointManager
	Decisions           session.DecisionStore
	GateRepeat          *approvalstate.GateRepeatLedger
	SandboxWriteRoot    *approvalstate.SandboxPathGrantRuntime
	SandboxReadPath     *approvalstate.SandboxPathGrantRuntime
	SandboxListen       *approvalstate.SandboxPortGrantRuntime
	SandboxLoopback     *approvalstate.SandboxPortGrantRuntime
	GrantedPath         *grantedpath.Runtime
	WorkerToolBudgetFor func(string) spawn.WorkerToolBudget
}
