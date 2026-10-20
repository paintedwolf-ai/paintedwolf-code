package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/toolschema"
)

type programDoorMemory struct{ held int64 }

func (m *programDoorMemory) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.held += n
	return func() { m.held -= n }, nil
}

type programDoorProgress struct{ store progress.Store }

func (p programDoorProgress) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type programDoorStart struct{ starts int }

func (p *programDoorStart) BeforeNativeOwner(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
	p.starts++
	return nil
}

// This binds the existing mutation door and source ledger through reached
// admission. It does not qualify parser dispatch or an implemented tee utility.
func TestProgramAdmissionPreservesActualFirstCreationAndMissingProgress(t *testing.T) {
	dir := t.TempDir()
	ledger := bindLedgerForWrites(t, dir)
	sink := &txProbeSink{}
	bindProbeSink(t, ledger, sink)
	tc := nativefixture.Context(dir)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID, tc.Identity.Agent, tc.Identity.UserTurn = "p1", "s1", "actual-model-call", "fixture", 1
	tc.Source.SourceLedger = ledger
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tc.Source.Commands, tc.Source.Observations, tc.Source.GitMutations = ledger.Commands, ledger.Inventory, ledger.Git
	tc.Source.SourceMutations = projectsource.NewSourceMutationService(ledger.LedgerDB(), ledger)
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	testutil.FailErr(t, "compile fixture profile", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), "fixture")
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "read actual write schema", err)
	meta, ok := schemas.ToolMeta("write")
	if !ok {
		t.Fatal("actual write schema missing")
	}
	encoded, err := json.Marshal(meta.ArgsSchema)
	testutil.FailErr(t, "freeze actual schema", err)
	contract, ok := toolcontract.Lookup("write")
	if !ok {
		t.Fatal("write contract missing")
	}
	definition, err := toolcontract.ToolOperation("write", encoded, contract)
	testutil.FailErr(t, "compile actual write declaration", err)
	parent := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "unit", ExecutionID: "parent"}, ActorID: "fixture-person", SessionID: "s1", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"}, SourceUnitID: "source", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	frame, err := toolexecution.FreezeProgramFrame(parent, tc)
	testutil.FailErr(t, "capture actual tree", err)
	child := parent
	child.Identity.ExecutionID = "write-child"
	child.Identity.ParentExecutionID = "parent"
	bound, err := toolcontract.BindOperation(definition, child, "tee", "each-target", nil)
	testutil.FailErr(t, "bind catalogued mutation", err)
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
	testutil.FailErr(t, "capture actual profile", err)
	owner := progress.NewMemoryStore()
	facts := programDoorProgress{owner}
	marker := &programDoorStart{}
	memory := &programDoorMemory{}
	effect := toolexecution.NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{OperationID: "write", ExecutionID: "write-child", Argv: []string{"tee", "new.txt"}}, Arguments: map[string]any{"path": "new.txt", "content": "created through existing owner\n"}}
	prepared := 0
	effect.PrepareReviewed = func(ctx context.Context, args map[string]any, actual tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
		prepared++
		paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, "")
		if err != nil {
			return toolexecution.PreparedNativeEffect{}, err
		}
		operand, err := paths.Bind(ctx, args["path"].(string), true)
		if err != nil {
			return toolexecution.PreparedNativeEffect{}, err
		}
		content := []byte(args["content"].(string))
		return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, actual tools.ToolContext) (toolcontract.ProducerResult, error) {
			target, err := operand.MutationTarget()
			if err != nil {
				return toolcontract.ProducerResult{}, err
			}
			err = applyAgentFile(ctx, actual, resolvedMutationTarget(target), content, nil, "")
			if err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, err
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
		}}, nil
	}
	result, err := executor.InvokeNative(t.Context(), frame, surface, memory, facts, marker, effect, tc)
	if err == nil || result.OwnerInvoked || prepared != 0 || marker.starts != 0 || ledgerRows(t, ledger) != 0 {
		t.Fatalf("missing progress reached mutation: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("missing progress created a file")
	}
	testutil.FailErr(t, "set actual progress owner", owner.Set("s1", "- [ ] Create fixture"))
	result, err = executor.InvokeNative(t.Context(), frame, surface, memory, facts, marker, effect, tc)
	testutil.FailErr(t, "invoke actual admitted mutation door", err)
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	testutil.FailErr(t, "read actual effect", err)
	if !result.OwnerInvoked || !result.OutputPublishable || result.Producer.EffectCertainty != toolcontract.EffectCompleted || string(data) != "created through existing owner\n" || ledgerRows(t, ledger) != 1 || probeRows(t, ledger) != 1 || marker.starts != 1 || memory.held != 0 {
		t.Fatalf("lost effect, attribution or reservation: %+v held%d", result, memory.held)
	}
}
