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
	frame, err := FreezeProgramFrame(parent, tc)
	testutil.FailErr(t, "freeze frame", err)
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
	admission := &stageAdmissionFixture{}
	malformed := errors.New("sealed syntax refused")
	proof.err = malformed
	if _, err := executor.PrepareProgramEnvelope(t.Context(), frame, proof, update, admission, tc); !errors.Is(err, malformed) {
		t.Fatalf("syntax failure lost: %v", err)
	}
	if owner.Get(t.Context(), parent.SessionID) != "" || admission.held != 0 {
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
		if id.ExecutionID != "progress-execution" || id.ProgramID != "program" || id.RootSessionID != "session" || model != "actual-model-call" || !strings.Contains(args.CanonicalArguments, "Create fixture") {
			t.Fatalf("wrong progress attribution: %+v %s %s", id, model, args.CanonicalArguments)
		}
		if owner.Get(t.Context(), parent.SessionID) != "" {
			t.Fatal("owner ran before marker")
		}
		return nil
	})
	outcome, err := executor.ApplyProgramProgress(t.Context(), prepared, owner, nativeProgressFixture{owner}, marker, tc)
	testutil.FailErr(t, "apply existing typed owner", err)
	if !outcome.OwnerInvoked || !outcome.Outcome.Updated || starts != 1 || owner.Get(t.Context(), parent.SessionID) != "- [ ] Create fixture" {
		t.Fatalf("lost separate progress outcome: %+v", outcome)
	}
	if _, err := executor.ApplyProgramProgress(t.Context(), prepared, owner, nativeProgressFixture{owner}, marker, tc); err == nil {
		t.Fatal("automatically replayed progress effect")
	}
	prepared.Close()
	if admission.held != 0 {
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
