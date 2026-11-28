package specsync

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

func wireTerminationReasons(r *invariants.Run) []string {
	enums, err := spec(r).Enums()
	must(r, "list OpenAPI enums", err)
	reasons := enums["BackgroundProcessTerminationReason"]
	r.Expect(len(reasons), "wire background process termination reasons")
	return reasons
}

func backgroundTerminationReasonsMatch(r *invariants.Run) {
	host := stringConsts(r, "internal/bgprocess", "TerminationReason")
	r.Diff("background process termination reasons", wireTerminationReasons(r), slices.Collect(maps.Values(host)))
}

func heldJobsPreserveStopReasons(r *invariants.Run) {
	for _, raw := range wireTerminationReasons(r) {
		reason := bgprocess.TerminationReason(raw)
		// Natural exit and deadline expiry are outcomes observed by the process owner.
		if reason == bgprocess.TerminationExited || reason == bgprocess.TerminationTimedOut {
			continue
		}
		checkHeldJobStop(r, reason, true)
		checkHeldJobStop(r, reason, false)
	}
}

// Publication is synchronous inside registration. Entry into the body occurs
// after registration. These are observable boundaries on either side of the
// handoff between the program supervisor and the process registry.
func checkHeldJobStop(r *invariants.Run, reason bgprocess.TerminationReason, stopAtPublication bool) {
	ctx := context.Background()
	const session, program = "session", "program"
	var handle string
	var identity context.Context
	var supervisor command.ProgramSupervisor
	stop := func() {
		supervisor.StopProgram(program, reason)
		// The identity retains the live program but carries no cancellation;
		// refusal here proves that the admission gate itself has closed.
		if supervisor.AdmitChild(identity) == nil {
			r.Report("%s (stop at publication=%t): child admitted at Stop return", reason, stopAtPublication)
		}
		if _, _, release, err := supervisor.Hold(identity); err == nil {
			release()
			r.Report("%s (stop at publication=%t): hold admitted at Stop return", reason, stopAtPublication)
		}
	}
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		PublishProject: func(_ context.Context, event api.ProjectBackgroundEvent) {
			if event.Action == "started" {
				handle = event.ProcessID
				if stopAtPublication {
					stop()
				}
			}
		},
	})
	supervisor.Background = registry
	defer func() { must(r, "close registry", registry.Lifecycle.Close(ctx)) }()
	jobs := supervisor.Jobs(session, "project")
	admitted, err := jobs.BeginProgram(ctx, command.ProgramJobOrigin{Tool: "command", ToolCallID: "call", RootSessionID: session})
	must(r, "admit program", err)
	root := toolcontract.OperationInvocation{SessionID: session, Identity: toolcontract.ExecutionIdentity{ProgramID: program}}
	var bodyRan bool
	_, runErr := supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
		identity = context.WithoutCancel(runCtx)
		_, err := supervisor.StartJob(runCtx, root, "held job", func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			bodyRan = true
			if !stopAtPublication {
				stop()
			}
			return toolcontract.ProducerResult{}, jobCtx.Err()
		})
		return toolcontract.ProducerResult{}, err
	})
	_, stopped, err := registry.Lifecycle.Wait(ctx, session, handle)
	must(r, "wait for held job", err)
	if !errors.Is(runErr, command.ErrProgramInterrupted) {
		r.Report("%s (stop at publication=%t): expected foreground interruption, got %v", reason, stopAtPublication, runErr)
	}
	snapshot, err := registry.Snapshot(ctx, session, handle, 0)
	must(r, "read held job outcome", err)
	if !stopped || snapshot.TerminationReason != reason || bodyRan == stopAtPublication {
		r.Report("%s (stop at publication=%t): held job stopped=%t reason=%q body ran=%t", reason, stopAtPublication, stopped, snapshot.TerminationReason, bodyRan)
	}
}
