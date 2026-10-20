package invocation_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// Accounting here belongs to the fixture; actual parser and admission objects
// must retain and release their real leases through the composed owner call.
type progressMemory struct{ held int64 }

func (m *progressMemory) ReserveParserScratch(_ context.Context, n int64) (func(), error) {
	return m.reserve(n), nil
}
func (m *progressMemory) ReserveProgramState(_ context.Context, n int64) (func(), error) {
	return m.reserve(n), nil
}
func (m *progressMemory) reserve(n int64) func() { m.held += n; return func() { m.held -= n } }

type progressFacts struct{ store progress.Store }

func (f progressFacts) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: f.store.Get(ctx, id)}, nil
}
func (r *retainedInputsFixture) RetainReviewedProgressInputs(_ context.Context, _ toolcontract.ProgramProgressIdentity, _ string) (string, error) {
	r.calls++
	return "output:screened-progress-review", nil
}

func TestProgramActualParserProgressOwnerAndReceiptRemainIndependent(t *testing.T) {
	for _, mode := range []string{"committed", "closed-plan", "invalid-update"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgramReceiptFixture(t)
			memory := &progressMemory{}
			parser, err := commandsurface.NewProgramParser(t.Context(), memory)
			testutil.FailErr(t, "reserve actual parser", err)
			plan, err := parser.Parse([]byte("${op} arg"), commandsurface.ParseOptions{})
			testutil.FailErr(t, "validate whole actual program", err)
			testutil.FailErr(t, "release parser scratch", parser.Close())
			defer func() {
				testutil.FailErr(t, "release actual plan", plan.Close())
				if memory.held != 0 {
					t.Fatalf("retained memory=%d", memory.held)
				}
			}()
			parent := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, NodeID: "unit", ExecutionID: f.root}, ActorID: "fixture-person", SessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "submitted", Span: f.span, CatalogDigest: toolcontract.LanguageCatalogDigest()}
			tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call", Agent: "fixture"}}
			frame, err := toolexecution.FreezeProgramFrame(parent, tc)
			testutil.FailErr(t, "freeze actual outer invocation", err)
			schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
			testutil.FailErr(t, "load real progress schema", err)
			registry, err := tools.NewCatalogRegistry(schemas)
			testutil.FailErr(t, "load real tool contracts", err)
			testutil.FailErr(t, "register actual progress metadata", registry.Register("update_progress", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				t.Fatal("recursive JSON dispatch")
				return "", nil
			}))
			executor := toolexecution.NewExecutor(nil, registry, "fixture")
			owner := progress.NewSQLStore(f.database)
			inputs := &retainedInputsFixture{}
			observer := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: inputs}
			update := &toolexecution.ProgramProgressUpdate{ExecutionID: uuid.NewString(), Arguments: map[string]any{"content": "- [ ] Create fixture"}}
			if mode == "invalid-update" {
				update.Arguments["unknown_field"] = true
			}
			prepared, err := executor.PrepareProgramEnvelope(t.Context(), frame, plan, update, memory, tc)
			if mode == "invalid-update" {
				if err == nil {
					prepared.Close()
					t.Fatal("invalid progress admitted")
				}
			} else {
				testutil.FailErr(t, "prepare actual parser and progress", err)
				defer prepared.Close()
				if mode == "closed-plan" {
					testutil.FailErr(t, "close plan before application", plan.Close())
				}
				result, applyErr := executor.ApplyProgramProgress(t.Context(), prepared, owner, progressFacts{owner}, observer, tc)
				if mode == "closed-plan" {
					if applyErr == nil || result.OwnerInvoked {
						t.Fatal("closed plan reached progress owner")
					}
				} else {
					testutil.FailErr(t, "apply actual SQL progress owner", applyErr)
					if !result.OwnerInvoked || !result.Outcome.Updated {
						t.Fatalf("missing actual update: %+v", result)
					}
					testutil.FailErr(t, "settle separate actual progress", f.recorder.SettleProgramProgress(t.Context(), invocation.ProgressSettlement{Identity: result.Identity, OwnerInvoked: true, State: "committed", DetailReference: "output:actual-progress-result"}))
					if err := f.recorder.SettleProgramProgress(t.Context(), invocation.ProgressSettlement{Identity: result.Identity, OwnerInvoked: true, State: "committed", DetailReference: "output:duplicate"}); err == nil {
						t.Fatal("duplicate progress settlement accepted")
					}
					testutil.FailErr(t, "record later program refusal", f.recorder.SettleProgram(t.Context(), f.program, invocation.ProgramSettlement{State: "rejected", StopCode: "COMMAND_OPERATION_NOT_ALLOWED", DetailReference: "output:later-refusal"}))
				}
			}
			page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
			testutil.FailErr(t, "read durable progress attribution", err)
			if mode == "committed" {
				if owner.Get(t.Context(), f.session) != "- [ ] Create fixture" || inputs.calls != 1 || page.Progress == nil || page.Progress.State != "committed" || page.Program.ProgressState != "committed" || page.Program.State != "rejected" {
					t.Fatalf("later refusal erased progress: %+v", page)
				}
			} else if owner.Get(t.Context(), f.session) != "" || inputs.calls != 0 || page.Progress != nil {
				t.Fatal("rejected envelope committed progress or receipt")
			}
		})
	}
}
