package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// This fixture invokes real source/editor/journal and supervision boundaries,
// without a shell evaluator, synthetic child model calls or model-observation reads.
func TestCommandCompositionPreservesPublicationAfterAggregateFailure(t *testing.T) {
	adapter, _, project, rootID := newAdapterFixture(t)
	ctx := t.Context()
	settleAggregate := beginFixtureAggregate(t)
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: project.ID, SessionID: "composition-session"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: rootID, Path: project.Roots[0].Path, IsPrimary: true}}, ActiveRootID: rootID, ProjectSourceBranch: project.SourceBranch, SourceWorkspaceKind: api.SourceWorkspaceKindProject, EditorDocuments: adapter},
	}
	resolved, err := projectpaths.ResolveRead(ctx, sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}}}), tctx, "a.txt")
	if err != nil {
		t.Fatalf("resolve input: %v", err)
	}
	captured, err := sourceview.LoadText(ctx, "read", sourceview.AccessRead, tctx, resolved)
	if err != nil || captured.Editor == nil {
		t.Fatalf("capture accepted snapshot: %+v %v", captured, err)
	}
	if captured.Content != "base\n" {
		t.Fatalf("captured=%q", captured.Content)
	}
	pin := toolcontract.SourcePin{SourceID: captured.Editor.ID, Revision: strconv.FormatInt(captured.Editor.Revision, 10), Representation: toolcontract.LogicalUTF8LF, SHA256: textfile.SHA256([]byte(captured.Content)), EndByte: int64(len(captured.Content))}
	transformed := strings.ToUpper(captured.Content)
	if pin.SHA256 != textfile.SHA256([]byte("base\n")) || transformed != "BASE\n" {
		t.Fatal("transform used a different snapshot")
	}
	if reads := adapter.FreezeAgentReads(project.ID, tctx.Identity.SessionID); len(reads) != 0 {
		t.Fatalf("internal capture seeded model evidence: %+v", reads)
	}
	edit := tools.EditorDocumentEdit{DocumentID: captured.Editor.ID, ExpectedRevision: captured.Editor.Revision, Content: transformed, OperationID: uuid.NewString(), SessionID: tctx.Identity.SessionID, ToolName: "write"}
	previews, err := adapter.PreviewAgentEdits(ctx, project.ID, []tools.EditorDocumentEdit{edit})
	if err != nil || len(previews) != 1 {
		t.Fatalf("prepare review: %v", err)
	}
	if previews[0].Before.SHA256 != captured.Editor.SHA256 || previews[0].After != "BASE\n" {
		t.Fatalf("reviewed different bytes: %+v", previews[0])
	}
	reviews := 0
	tctx.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		reviews++
		if len(changes) != 1 || changes[0].Preview.Before != "base\n" || changes[0].Preview.After != "BASE\n" {
			return errors.New("review did not receive captured proposal")
		}
		before, err := os.ReadFile(changes[0].Path)
		if err != nil {
			return err
		}
		if string(before) != "base\n" {
			return errors.New("publication preceded review")
		}
		return nil
	}
	if err := tctx.ReviewFileChanges(ctx, tools.FileChange{Path: resolved.Abs, Preview: api.ApprovalFileChange{Path: "a.txt", RootID: rootID, Operation: "write", Before: previews[0].Before.Text, After: previews[0].After, BeforeSHA256: previews[0].Before.SHA256, AfterSHA256: previews[0].AfterSHA256}}); err != nil {
		t.Fatalf("review prepared publication: %v", err)
	}
	if reviews != 1 {
		t.Fatalf("review count=%d", reviews)
	}
	edit.ReviewedRevision = previews[0].Before.Revision
	var publication tools.EditorDocumentApplied
	var ownerError error
	supervisor := heldcall.New(nil, nil)
	t.Cleanup(func() {
		if err := supervisor.Close(context.Background()); err != nil {
			t.Errorf("close supervision: %v", err)
		}
	})
	outcome, err := supervisor.Run(ctx, heldcall.Spec{ProjectID: project.ID, SessionID: tctx.Identity.SessionID, ToolCallID: "model-command-call", Tool: "command", ArgsDigest: "fixture-composition", Budget: time.Minute}, func(opCtx context.Context) heldcall.Settled {
		publication, ownerError = adapter.ApplyAgentEdit(opCtx, project.ID, edit)
		// A later stage fails. The earlier owner result remains authoritative.
		return heldcall.Settled{Tool: "command", Outcome: api.ToolResultOutcomeError, Content: "fixture later stage failed"}
	})
	if err != nil || ownerError != nil || outcome.Settled == nil || outcome.Settled.Outcome != api.ToolResultOutcomeError {
		t.Fatalf("aggregate=%+v err=%v owner=%v", outcome, err, ownerError)
	}
	if !publication.Saved || publication.PublicationError != nil || publication.Document.Revision <= captured.Editor.Revision {
		t.Fatalf("publication=%+v", publication)
	}
	raw, err := os.ReadFile(filepath.Join(project.Roots[0].Path, "a.txt"))
	if err != nil || string(raw) != "BASE\n" {
		t.Fatalf("published bytes=%q err=%v", raw, err)
	}
	if reads := adapter.FreezeAgentReads(project.ID, tctx.Identity.SessionID); len(reads) != 0 {
		t.Fatal("internal publication fabricated delivered observation")
	}
	settleAggregate(outcome.Settled.Outcome)
}

func beginFixtureAggregate(t *testing.T) func(api.ToolResultOutcome) {
	t.Helper()
	database := testdbfixture.Open(t, "aggregate.db")
	testdbseed.InsertSession(t, database, "composition-session", testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(database)
	contract, _ := toolcontract.Lookup("command")
	if contract.Owner != "processes" {
		t.Fatal("aggregate changed owner")
	}
	receipt, err := recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: "composition-session", ToolCallID: "model-command-call", ToolName: "command", Args: map[string]any{"cmd": "fixture"}, Contract: contract})
	if err != nil {
		t.Fatalf("begin aggregate: %v", err)
	}
	return func(outcome api.ToolResultOutcome) {
		if outcome != api.ToolResultOutcomeError {
			t.Fatalf("unexpected aggregate outcome: %s", outcome)
		}
		settlement := invocation.Settlement{Status: api.InvocationStatusError, Invoked: true, EvidenceKind: string(contract.Evidence()), Failure: &api.InvocationFailure{Code: "TOOL_OWNER_FAILED", Class: "owner_error"}}
		if _, err := recorder.Settle(t.Context(), receipt.ID, settlement); err != nil {
			t.Fatalf("settle aggregate: %v", err)
		}
		var refused *invocation.SettlementRefusedError
		if _, err := recorder.Settle(t.Context(), receipt.ID, settlement); !errors.As(err, &refused) {
			t.Fatalf("duplicate aggregate settlement=%v", err)
		}
		rows, err := recorder.ListSession(t.Context(), "composition-session")
		if err != nil || len(rows) != 1 {
			t.Fatalf("aggregate rows=%d err=%v", len(rows), err)
		}
	}

}
