package toolexecution

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	testutil.FailErr(t, "register existing progress contract", registry.Register("update_progress", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		t.Fatal("JSON handler recursively invoked")
		return "", nil
	}))
	executor := NewExecutor(nil, registry, "fixture")
	store := progresstest.New(t, "session")
	proof := &envelopeIdentityFixture{catalog: parent.CatalogDigest}
	update := &ProgramProgressUpdate{
		ExecutionID: "progress-execution",
		Arguments:   map[string]any{"replace": []any{map[string]any{"label": "Create fixture"}}},
	}
	admission := stageMemory(t, 64<<20)
	malformed := errors.New("sealed syntax refused")
	proof.err = malformed
	if _, err := executor.PrepareProgramEnvelope(t.Context(), frame, proof, update, store, nativeProgressFixture{store}, admission, tc); !errors.Is(err, malformed) {
		t.Fatalf("syntax failure lost: %v", err)
	}
	snap, err := store.Snapshot(t.Context(), parent.SessionID)
	testutil.FailErr(t, "snapshot", err)
	if len(snap.Rows) != 0 || admission.InUse().State != 0 {
		t.Fatal("rejected syntax committed progress or retained state")
	}
	proof.err = nil
	invalid := *update
	invalid.Arguments = map[string]any{"replace": []any{map[string]any{"label": "Valid"}}, "unknown_field": true}
	if _, err := executor.PrepareProgramEnvelope(t.Context(), frame, proof, &invalid, store, nativeProgressFixture{store}, admission, tc); err == nil {
		t.Fatal("invalid progress schema accepted")
	}
	prepared, err := executor.PrepareProgramEnvelope(t.Context(), frame, proof, update, store, nativeProgressFixture{store}, admission, tc)
	testutil.FailErr(t, "prepare both valid units", err)
	defer prepared.Close()
	update.Arguments["replace"] = []any{map[string]any{"label": "Caller tampering", "state": "done"}}
	starts := 0
	marker := progressStartFunc(func(ctx context.Context, id toolcontract.ProgramProgressIdentity, model string, args toolcontract.ReviewedOperationInputs) error {
		starts++
		if id.ExecutionID != "progress-execution" || id.ProgramID != "program" || id.RootSessionID != "session" || model != "actual-model-call" || !strings.Contains(args.CanonicalArguments(), "Create fixture") {
			t.Fatalf("wrong progress attribution: %+v %s %s", id, model, args.CanonicalArguments())
		}
		cur, curErr := store.Snapshot(t.Context(), parent.SessionID)
		if curErr != nil || len(cur.Rows) != 0 {
			t.Fatal("store updated before marker")
		}
		if _, ok := people.Caller(ctx); ok {
			t.Fatal("progress owner saw the request caller")
		}
		return nil
	})
	callerCtx := people.WithCaller(t.Context(), people.Person{ID: "person-1", Role: api.PersonRoleOwner})
	outcome, err := executor.ApplyProgramProgress(callerCtx, prepared, store, nativeProgressFixture{store}, nil, marker, tc)
	testutil.FailErr(t, "apply existing typed progress store", err)
	after, err := store.Snapshot(t.Context(), parent.SessionID)
	testutil.FailErr(t, "snapshot after apply", err)
	if !outcome.OwnerInvoked || !outcome.Outcome.Updated || starts != 1 || len(after.Rows) != 1 || after.Rows[0].Label != "Create fixture" {
		t.Fatalf("lost separate progress outcome: %+v", outcome)
	}
	if _, err := executor.ApplyProgramProgress(t.Context(), prepared, store, nativeProgressFixture{store}, nil, marker, tc); err == nil {
		t.Fatal("automatically replayed progress effect")
	}
	prepared.Close()
	if admission.InUse().State != 0 {
		t.Fatal("review snapshot lease leaked")
	}
	definition := nativeDefinition(t, "write")
	if err := CheckOperationProgress(definition, parent, true, false, false); err == nil {
		t.Fatal("missing progress fixture failed to reject")
	}
	afterReject, err := store.Snapshot(t.Context(), parent.SessionID)
	testutil.FailErr(t, "snapshot after reject", err)
	if len(afterReject.Rows) != 1 || afterReject.Rows[0].Label != "Create fixture" {
		t.Fatal("program rejection erased committed progress")
	}
}

func TestHeldProgramEnvelopeRejectsRelabeledRowAndInterveningReplace(t *testing.T) {
	t.Run("relabeled-row", func(t *testing.T) {
		parent, tc := frameFixture(t)
		frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
		testutil.FailErr(t, "freeze frame", err)
		defer frame.Close()
		schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
		testutil.FailErr(t, "load progress schema", err)
		registry, err := tools.NewCatalogRegistry(schemas)
		testutil.FailErr(t, "bind catalog", err)
		testutil.FailErr(t, "register progress metadata", registry.Register("update_progress", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("recursive progress handler dispatch")
			return "", nil
		}))
		executor := NewExecutor(nil, registry, "fixture")
		store := progresstest.New(t, "session")
		progresstest.SeedRows(t, store, "session",
			progress.RowSpec{Label: "Survey"},
			progress.RowSpec{Label: "Implement"},
		)

		update := &ProgramProgressUpdate{
			ExecutionID: "progress-relabel",
			Arguments:   map[string]any{"update": []any{map[string]any{"id": "p2", "state": "done"}}},
		}
		prepared, err := executor.PrepareProgramEnvelope(t.Context(), frame, &envelopeIdentityFixture{catalog: parent.CatalogDigest}, update, store, nativeProgressFixture{store}, stageMemory(t, 64<<20), tc)
		testutil.FailErr(t, "prepare update envelope", err)
		defer prepared.Close()

		// Relabel p2 while the prepared envelope is held.
		progresstest.UpdateRows(t, store, "session", progress.UpdateRowSpec{
			ID: "p2", Label: progresstest.Label("Audit security boundary"),
		})

		marker := progressStartFunc(func(context.Context, toolcontract.ProgramProgressIdentity, string, toolcontract.ReviewedOperationInputs) error {
			return nil
		})
		outcome, err := executor.ApplyProgramProgress(t.Context(), prepared, store, nativeProgressFixture{store}, nil, marker, tc)
		var rejected *toolrejection.ToolReject
		if !errors.As(err, &rejected) || rejected.Code != "PROGRESS_STALE_BASIS" || !outcome.OwnerInvoked || outcome.Outcome.Updated {
			t.Fatalf("stale relabeled update = %+v / %v, want PROGRESS_STALE_BASIS", outcome, err)
		}
		changedIDs, _ := rejected.Data["changed_ids"].([]string)
		if !slices.Equal(changedIDs, []string{"p2"}) {
			t.Fatalf("changed_ids = %v, want [p2]", changedIDs)
		}
		after, err := store.Snapshot(t.Context(), "session")
		testutil.FailErr(t, "snapshot", err)
		if after.Rows[1].State != progress.ProgressStatePending || after.Rows[1].Label != "Audit security boundary" {
			t.Fatalf("relabeled row closed by stale call: %+v", after.Rows[1])
		}
	})

	t.Run("intervening-replace", func(t *testing.T) {
		parent, tc := frameFixture(t)
		frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
		testutil.FailErr(t, "freeze frame", err)
		defer frame.Close()
		schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
		testutil.FailErr(t, "load progress schema", err)
		registry, err := tools.NewCatalogRegistry(schemas)
		testutil.FailErr(t, "bind catalog", err)
		testutil.FailErr(t, "register progress metadata", registry.Register("update_progress", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("recursive progress handler dispatch")
			return "", nil
		}))
		executor := NewExecutor(nil, registry, "fixture")
		store := progresstest.New(t, "session")
		progresstest.SeedRows(t, store, "session", progress.RowSpec{Label: "Initial plan"})

		replaceCall := &ProgramProgressUpdate{
			ExecutionID: "progress-replace",
			Arguments:   map[string]any{"replace": []any{map[string]any{"label": "Delayed replacement"}}},
		}
		prepared, err := executor.PrepareProgramEnvelope(t.Context(), frame, &envelopeIdentityFixture{catalog: parent.CatalogDigest}, replaceCall, store, nativeProgressFixture{store}, stageMemory(t, 64<<20), tc)
		testutil.FailErr(t, "prepare replace envelope", err)
		defer prepared.Close()

		// Advance document_version with a newer plan while the prepared replace waits.
		progresstest.SeedRows(t, store, "session", progress.RowSpec{Label: "Newer committed plan"})

		marker := progressStartFunc(func(context.Context, toolcontract.ProgramProgressIdentity, string, toolcontract.ReviewedOperationInputs) error {
			return nil
		})
		outcome, err := executor.ApplyProgramProgress(t.Context(), prepared, store, nativeProgressFixture{store}, nil, marker, tc)
		var rejected *toolrejection.ToolReject
		if !errors.As(err, &rejected) || rejected.Code != "PROGRESS_STALE_BASIS" || !outcome.OwnerInvoked || outcome.Outcome.Updated {
			t.Fatalf("stale replace = %+v / %v, want PROGRESS_STALE_BASIS", outcome, err)
		}
		after, err := store.Snapshot(t.Context(), "session")
		testutil.FailErr(t, "snapshot", err)
		if len(after.Rows) != 1 || after.Rows[0].Label != "Newer committed plan" {
			t.Fatalf("delayed replace overwrote newer plan: %+v", after.Rows)
		}
	})
}
