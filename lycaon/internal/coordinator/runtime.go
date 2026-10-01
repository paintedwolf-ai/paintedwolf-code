package coordinator

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

// RuntimeDeps wires the coordinator prompt runtime.
type RuntimeDeps struct {
	LoopDeps     func() promptloop.PromptLoopDeps
	AssemblyDeps func() assembly.AssemblyDeps
	LoopWakeDeps func() loopwake.LoopDeps
}

// completionAssembler builds LLM completion messages for one coordinator turn.
type completionAssembler interface {
	BuildCompletionMessages(ctx context.Context, sess *api.Session, history []api.Message, frame *inject.CoordinatorTurnFrame) ([]api.Message, error)
	BeginPromptTurn(sessionID string, pendingKickIDs ...string)
	EndPromptTurn(sessionID string)
	SetTurnSurfaceID(sessionID, surfaceID string)
	TurnSurfaceID(sessionID string) string
}

// Runtime composes PromptLoop, assembly, kicks, board inject, and the coordinator loop.
type Runtime struct {
	depsMu             sync.Mutex
	loop               *promptloop.PromptLoop
	loopDeps           func() promptloop.PromptLoopDeps
	assembler          completionAssembler
	assemblyDepsFn     func() assembly.AssemblyDeps
	kicks              *kick.KickEngine
	anchors            *anchor.Bus
	board              *assembly.BoardEngine
	coordLoop          *loopwake.LoopEngine
	loopWakeDeps       func() loopwake.LoopDeps
	executionModeStore *surface.ExecutionModeStateStore
}

// NewRuntime constructs a Runtime. deps refresh on each RunPrompt call.
func NewRuntime(deps RuntimeDeps) *Runtime {
	kicks := &kick.KickEngine{}
	return &Runtime{
		loop:               &promptloop.PromptLoop{},
		loopDeps:           deps.LoopDeps,
		assembler:          &assembly.AssemblyEngine{},
		assemblyDepsFn:     deps.AssemblyDeps,
		kicks:              kicks,
		anchors:            anchor.NewBus(kicks),
		board:              assembly.NewBoardEngine(nil, nil, nil),
		coordLoop:          loopwake.NewLoopEngine(),
		loopWakeDeps:       deps.LoopWakeDeps,
		executionModeStore: surface.NewExecutionModeStateStore(),
	}
}

func (r *Runtime) refreshDepsLocked() {
	if r == nil {
		return
	}
	if r.coordLoop == nil {
		r.coordLoop = loopwake.NewLoopEngine()
	}
	if r.loopDeps != nil {
		if r.loop == nil {
			r.loop = &promptloop.PromptLoop{}
		}
		r.loop.Deps = r.loopDeps()
		// Direct binding avoids reentering depsMu.
		r.loop.Deps.ObservePrompt = r.coordLoop.ObservePrompt
		r.loop.Deps.CommitWorkerContext = r.assemblyEngineLocked().CommitWorkerContext
	}
	if r.assemblyDepsFn != nil {
		deps := r.assemblyDepsFn()
		eng := r.assemblyEngineLocked()
		eng.SetDeps(deps)
		if r.board != nil {
			r.board.SetInjectRenderer(deps.Injects)
		}
		if r.kicks != nil && deps.Prompts != nil {
			r.kicks.SetPromptEngine(deps.Prompts)
		}
	}
	if r.loopWakeDeps != nil {
		r.coordLoop.SetDeps(r.loopWakeDeps())
	}
}

// RunPrompt executes the tool iteration loop for a session turn.
func (r *Runtime) RunPrompt(ctx context.Context, in promptloop.PromptRunInput) (*promptloop.PromptRunResult, error) {
	if r == nil {
		return nil, errRuntimeNotConfigured
	}
	r.depsMu.Lock()
	r.refreshDepsLocked()
	if r.loop == nil {
		r.depsMu.Unlock()
		return nil, errRuntimeNotConfigured
	}
	runLoop := *r.loop
	r.depsMu.Unlock()
	return runLoop.Run(ctx, in)
}

// PromptLoop returns the loop instance with deps refreshed (tests and parity checks).
func (r *Runtime) PromptLoop() *promptloop.PromptLoop {
	if r == nil {
		return &promptloop.PromptLoop{}
	}
	r.depsMu.Lock()
	defer r.depsMu.Unlock()
	r.refreshDepsLocked()
	if r.loop == nil {
		r.loop = &promptloop.PromptLoop{}
	}
	return r.loop
}

func (r *Runtime) assemblyEngineLocked() *assembly.AssemblyEngine {
	if r.assembler == nil {
		r.assembler = &assembly.AssemblyEngine{}
	}
	eng, ok := r.assembler.(*assembly.AssemblyEngine)
	if !ok {
		eng = &assembly.AssemblyEngine{}
		r.assembler = eng
	}
	return eng
}

// Assembly returns the assembly engine with deps refreshed.
func (r *Runtime) Assembly() *assembly.AssemblyEngine {
	if r == nil {
		return &assembly.AssemblyEngine{}
	}
	r.depsMu.Lock()
	defer r.depsMu.Unlock()
	r.refreshDepsLocked()
	return r.assemblyEngineLocked()
}

// Kicks returns the guidance queue and renderer.
func (r *Runtime) Kicks() *kick.KickEngine {
	if r == nil {
		return &kick.KickEngine{}
	}
	if r.kicks == nil {
		r.kicks = &kick.KickEngine{}
	}
	if r.anchors == nil {
		r.anchors = anchor.NewBus(r.kicks)
	}
	return r.kicks
}

// Anchors returns the Emit bus (single inform-plane emission API).
func (r *Runtime) Anchors() *anchor.Bus {
	if r == nil {
		return anchor.NewBus(&kick.KickEngine{})
	}
	if r.kicks == nil {
		r.kicks = &kick.KickEngine{}
	}
	if r.anchors == nil {
		r.anchors = anchor.NewBus(r.kicks)
	} else if r.anchors.Kicks() != r.kicks {
		r.anchors = anchor.NewBus(r.kicks)
	}
	return r.anchors
}

// Board returns the board inject engine.
func (r *Runtime) Board() *assembly.BoardEngine {
	if r == nil {
		return assembly.NewBoardEngine(nil, nil, nil)
	}
	if r.board == nil {
		r.board = assembly.NewBoardEngine(nil, nil, nil)
	}
	return r.board
}

// CoordinatorLoop returns the sleep/wake loop engine with deps refreshed.
func (r *Runtime) CoordinatorLoop() *loopwake.LoopEngine {
	if r == nil {
		return loopwake.NewLoopEngine()
	}
	r.depsMu.Lock()
	defer r.depsMu.Unlock()
	r.refreshDepsLocked()
	if r.coordLoop == nil {
		r.coordLoop = loopwake.NewLoopEngine()
	}
	return r.coordLoop
}

// ForgetSession releases coordinator runtime state for a deleted session.
func (r *Runtime) ForgetSession(ctx context.Context, sessionID string) {
	if r == nil {
		return
	}
	r.depsMu.Lock()
	loop := r.coordLoop
	r.depsMu.Unlock()
	if loop != nil {
		loop.ForgetSession(ctx, sessionID)
	}
	r.Kicks().ForgetSession(sessionID)
}

// DrainLoopPending runs deferred loop wakes with deps refreshed.
func (r *Runtime) DrainLoopPending(ctx context.Context, sessionID string) {
	if r == nil {
		return
	}
	r.depsMu.Lock()
	r.refreshDepsLocked()
	loop := r.coordLoop
	r.depsMu.Unlock()
	if loop != nil {
		loop.DrainPending(ctx, sessionID)
	}
}

// BeginPromptTurn starts cached assembly for a session turn that delivers
// pendingKickIDs.
func (r *Runtime) BeginPromptTurn(sessionID string, pendingKickIDs ...string) {
	if r == nil {
		return
	}
	r.depsMu.Lock()
	r.refreshDepsLocked()
	assembler := r.assembler
	r.depsMu.Unlock()
	if assembler != nil {
		assembler.BeginPromptTurn(sessionID, pendingKickIDs...)
	}
}

// EndPromptTurn clears cached assembly state.
func (r *Runtime) EndPromptTurn(sessionID string) {
	if r == nil {
		return
	}
	r.depsMu.Lock()
	assembler := r.assembler
	r.depsMu.Unlock()
	if assembler != nil {
		assembler.EndPromptTurn(sessionID)
	}
}

// PromptTurnSurfaceID returns the coordinator surface for the active prompt turn.
func (r *Runtime) PromptTurnSurfaceID(sessionID string) string {
	if r == nil {
		return ""
	}
	r.depsMu.Lock()
	assembler := r.assembler
	r.depsMu.Unlock()
	if assembler != nil {
		return assembler.TurnSurfaceID(sessionID)
	}
	return ""
}

// BuildCompletionMessages releases depsMu before reentrant assembly hooks.
func (r *Runtime) BuildCompletionMessages(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	frame *inject.CoordinatorTurnFrame,
) ([]api.Message, error) {
	if r == nil {
		return history, nil
	}
	r.depsMu.Lock()
	r.refreshDepsLocked()
	assembler := r.assembler
	r.depsMu.Unlock()
	if assembler == nil {
		return history, nil
	}
	return assembler.BuildCompletionMessages(ctx, sess, history, frame)
}

// PushModeTransitionCause records the next execution-mode entry.
func (r *Runtime) PushModeTransitionCause(sessionID string, cause surface.ModeTransitionCause) {
	if r == nil {
		return
	}
	r.depsMu.Lock()
	assembler := r.assembler
	r.depsMu.Unlock()
	if eng, ok := assembler.(*assembly.AssemblyEngine); ok && eng != nil {
		eng.Cache().PushModeTransitionCause(sessionID, cause)
	}
}

// ExecutionModeStore returns the host-only LastFamily store (tests).
func (r *Runtime) ExecutionModeStore() *surface.ExecutionModeStateStore {
	if r == nil {
		return surface.NewExecutionModeStateStore()
	}
	if r.executionModeStore == nil {
		r.executionModeStore = surface.NewExecutionModeStateStore()
	}
	return r.executionModeStore
}

// SetBoardInject wires board snapshot builder and formatter.
func (r *Runtime) SetBoardInject(builder assembly.BoardSnapshotBuilder, formatter assembly.BoardPackFormatter, omitDelegation func() bool) {
	r.Board().SetBuilder(builder, formatter)
	if r.board != nil {
		r.board.SetOmitDelegation(omitDelegation)
	}
}

// SetIncludeScanLegend configures whether scan legend is included in board inject.
func (r *Runtime) SetIncludeScanLegend(fn func() bool) {
	if r == nil {
		return
	}
	r.Board().SetIncludeScanLegend(fn)
}

// SetPromotePathOverlay supplies cached promotion path status to the board.
func (r *Runtime) SetPromotePathOverlay(fn func(sessionID string) []api.WorkerPromoteJobPathStatus) {
	if r == nil {
		return
	}
	r.Board().SetPromotePathOverlay(fn)
}

// SetOverlayMergePlan wires session-level overlay overlap planning into pack board inject.
func (r *Runtime) SetOverlayMergePlan(fn func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan) {
	if r == nil {
		return
	}
	r.Board().SetOverlayMergePlan(fn)
}

// SetActiveReservations wires handoff_reserve holds into pack board inject.
func (r *Runtime) SetActiveReservations(fn func(sessionID string) []api.BoardReservationEntry) {
	if r == nil {
		return
	}
	r.Board().SetActiveReservations(fn)
}
