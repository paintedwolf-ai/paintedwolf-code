package toolexecution

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// This exercises the structural consumer contract. Actual parser-seal lifecycle
// qualification belongs to the composed parser/entry fixture, not this stub.
type envelopeIdentityFixture struct {
	catalog string
	err     error
}

func (f *envelopeIdentityFixture) ValidatedProgramIdentity(context.Context) (string, string, error) {
	return strings.Repeat("a", 64), f.catalog, f.err
}

type progressStartFunc func(context.Context, toolcontract.ProgramProgressIdentity, string, toolcontract.ReviewedOperationInputs) error

func (f progressStartFunc) BeforeProgressOwner(ctx context.Context, id toolcontract.ProgramProgressIdentity, parent string, args toolcontract.ReviewedOperationInputs) error {
	return f(ctx, id, parent, args)
}

func TestProgramEnvelopeValidatesBeforeProgressAndSeparatesCommittedOutcome(t *testing.T) {
	parent, tc := frameFixture(t)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
	testutil.FailErr(t, "freeze frame", err)
	t.Cleanup(frame.Close)
	schema, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "read actual progress schema", err)
	registry, err := tools.NewCatalogRegistry(schema)
	testutil.FailErr(t, "bind actual catalog", err)
	testutil.FailErr(t, "register existing owner contract", registry.Register("update_progress", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		t.Fatal("JSON handler recursively invoked")
		return "", nil
	}))
	executor := NewExecutor(nil, registry, "fixture")
	owner := progress.NewMemoryStore()
	proof := &envelopeIdentityFixture{catalog: parent.CatalogDigest}
	update := &ProgramProgressUpdate{ExecutionID: "progress-execution", Arguments: map[string]any{"content": "- [ ] Create fixture"}}
	admission := stageMemory(t, 64<<20)
	malformed := errors.New("sealed syntax refused")
	proof.err = malformed
	if _, err := executor.PrepareProgramEnvelope(t.Context(), frame, proof, update, admission, tc); !errors.Is(err, malformed) {
		t.Fatalf("syntax failure lost: %v", err)
	}
	if owner.Get(t.Context(), parent.SessionID) != "" || admission.InUse().State != 0 {
		t.Fatal("rejected syntax committed progress or retained state")
	}
	proof.err = nil
	invalid := *update
	invalid.Arguments = map[string]any{"content": "- [ ] Valid", "unknown_field": true}
	if _, err := executor.PrepareProgramEnvelope(t.Context(), frame, proof, &invalid, admission, tc); err == nil {
		t.Fatal("invalid progress schema accepted")
	}
	prepared, err := executor.PrepareProgramEnvelope(t.Context(), frame, proof, update, admission, tc)
	testutil.FailErr(t, "prepare both valid units", err)
	defer prepared.Close()
	update.Arguments["content"] = "- [x] Caller tampering"
	starts := 0
	marker := progressStartFunc(func(_ context.Context, id toolcontract.ProgramProgressIdentity, model string, args toolcontract.ReviewedOperationInputs) error {
		starts++
		if id.ExecutionID != "progress-execution" || id.ProgramID != "program" || id.RootSessionID != "session" || model != "actual-model-call" || !strings.Contains(args.CanonicalArguments(), "Create fixture") {
			t.Fatalf("wrong progress attribution: %+v %s %s", id, model, args.CanonicalArguments())
		}
		if owner.Get(t.Context(), parent.SessionID) != "" {
			t.Fatal("owner ran before marker")
		}
		return nil
	})
	outcome, err := executor.ApplyProgramProgress(t.Context(), prepared, owner, nativeProgressFixture{owner}, nil, marker, tc)
	testutil.FailErr(t, "apply existing typed owner", err)
	if !outcome.OwnerInvoked || !outcome.Outcome.Updated || starts != 1 || owner.Get(t.Context(), parent.SessionID) != "- [ ] Create fixture" {
		t.Fatalf("lost separate progress outcome: %+v", outcome)
	}
	if _, err := executor.ApplyProgramProgress(t.Context(), prepared, owner, nativeProgressFixture{owner}, nil, marker, tc); err == nil {
		t.Fatal("automatically replayed progress effect")
	}
	prepared.Close()
	if admission.InUse().State != 0 {
		t.Fatal("review snapshot lease leaked")
	}
	// A later program rejection does not roll back this separately committed owner.
	definition := nativeDefinition(t, "write")
	if err := CheckOperationProgress(definition, parent, "", false, false); err == nil {
		t.Fatal("missing progress fixture failed to reject")
	}
	if owner.Get(t.Context(), parent.SessionID) != "- [ ] Create fixture" {
		t.Fatal("program rejection erased committed progress")
	}
}

func TestProgramEnvelopeChecksTheSharedOwnersObservedRevision(t *testing.T) {
	for _, mode := range []string{"stale", "missing"} {
		t.Run(mode, func(t *testing.T) {
			parent, tc := frameFixture(t)
			frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
			testutil.FailErr(t, "freeze actual model frame", err)
			defer frame.Close()
			schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
			testutil.FailErr(t, "load current owner schema", err)
			registry, err := tools.NewCatalogRegistry(schemas)
			testutil.FailErr(t, "bind owner catalog", err)
			testutil.FailErr(t, "register owner metadata", registry.Register("update_progress", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				t.Fatal("recursive progress handler dispatch")
				return "", nil
			}))
			executor := NewExecutor(nil, registry, "fixture")
			owner := progress.NewMemoryStore()
			testutil.FailErr(t, "seed pending deliverable", owner.Set("session", "- [ ] Deliver"))
			before, err := owner.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "observe current revision", err)
			update := &ProgramProgressUpdate{ExecutionID: "progress-revision", Arguments: map[string]any{"content": "- [x] Deliver", "expected_revision": before.Revision}}
			if mode == "missing" {
				delete(update.Arguments, "expected_revision")
			}
			prepared, err := executor.PrepareProgramEnvelope(t.Context(), frame, &envelopeIdentityFixture{catalog: parent.CatalogDigest}, update, stageMemory(t, 64<<20), tc)
			testutil.FailErr(t, "prepare revision-aware progress envelope", err)
			defer prepared.Close()
			testutil.FailErr(t, "publish newer scope", owner.Set("session", "- [ ] Deliver\n- [ ] Verify"))
			marker := progressStartFunc(func(context.Context, toolcontract.ProgramProgressIdentity, string, toolcontract.ReviewedOperationInputs) error {
				return nil
			})
			outcome, err := executor.ApplyProgramProgress(t.Context(), prepared, owner, nativeProgressFixture{owner}, nil, marker, tc)
			var rejected *toolrejection.ToolReject
			wantCode := "PROGRESS_REVISION_CHANGED"
			if mode == "missing" {
				wantCode = "PROGRESS_REVISION_REQUIRED"
			}
			if !errors.As(err, &rejected) || rejected.Code != wantCode || !outcome.OwnerInvoked || outcome.Outcome.Updated {
				t.Fatalf("stale composed update: %+v / %v", outcome, err)
			}
			if owner.Get(t.Context(), "session") != "- [ ] Deliver\n- [ ] Verify" {
				t.Fatal("composed progress bypassed the shared owner's revision")
			}
		})
	}
}
