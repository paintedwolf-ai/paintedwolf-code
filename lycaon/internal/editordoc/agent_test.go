package editordoc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type agentFixture struct {
	externalFixture
	recorder *captureRecorder
}

func newAgentFixture(t *testing.T, files map[string]string) agentFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	for name, content := range files {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	store := NewStore(sqlDB)
	recorder := &captureRecorder{Store: sourceledger.New(sqlDB, "")}
	service := New(store, recorder, recorder.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	changes := &[]Change{}
	service.SetOnChange(func(_ context.Context, c Change) { *changes = append(*changes, c) })
	return agentFixture{
		externalFixture: externalFixture{service: service, store: store, project: p, rootID: rootID, root: root, changes: changes},
		recorder:        recorder,
	}
}

func (f agentFixture) disk(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, name))
	testutil.FailErr(t, "read "+name, err)
	return string(raw)
}

func agentEdit(document *Document, content string) AgentEdit {
	return AgentEdit{
		ProjectID: document.ProjectID, DocumentID: document.ID, ExpectedRevision: document.Revision, Content: content,
		OperationID: uuid.NewString(), SessionID: "chat-1", Turn: 4, ToolCallID: "call-1", ToolName: "edit",
	}
}

func TestApplyAgentEditLandsInTheDocumentAndSaves(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.go": "package a\n"})
	document := f.open(t, "a.go")
	*f.changes = nil

	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "package a\n\nfunc F() {}\n"))
	testutil.FailErr(t, "apply", err)
	if !result.Saved || result.Document.Dirty || result.Document.Draft != "package a\n\nfunc F() {}\n" {
		t.Fatalf("result = %+v", result)
	}
	if got := f.disk(t, "a.go"); got != "package a\n\nfunc F() {}\n" {
		t.Fatalf("disk = %q", got)
	}
	if len(f.recorder.records) != 1 {
		t.Fatalf("ledger records = %d, want 1", len(f.recorder.records))
	}
	record := f.recorder.records[0]
	if record.Origin != api.SourceChangeOriginAgent || record.ToolCallID != "call-1" || record.ToolName != "edit" || record.Turn != 4 || record.SessionID != "chat-1" {
		t.Fatalf("ledger record = %+v, want the agent's tool call", record)
	}
	if len(*f.changes) != 2 || !(*f.changes)[0].ContentChanged {
		t.Fatalf("document transitions = %+v, want accepted text followed by publication", *f.changes)
	}
	if _, err := f.store.Mutation(t.Context(), (*f.changes)[0].Document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("document id is not a mutation id: %v", err)
	}
}

func TestApplyAgentEditKeepsThePersonsUnsavedEdits(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	document := f.open(t, "a.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "one typed\ntwo\nthree\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type", err)

	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(typed, "one typed\ntwo\nthree agent\n"))
	testutil.FailErr(t, "apply", err)
	if !result.Saved || f.disk(t, "a.txt") != "one typed\ntwo\nthree agent\n" {
		t.Fatalf("result = %+v disk = %q", result, f.disk(t, "a.txt"))
	}
}

func TestApplyAgentEditRefusesAMovedRevision(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	_, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "typed\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type", err)

	_, err = f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "agent\n"))
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("apply against a stale revision = %v, want ErrRevisionConflict", err)
	}
	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload", err)
	if stored.Draft != "typed\n" || f.disk(t, "a.txt") != "base\n" {
		t.Fatalf("stale apply touched state: draft=%q disk=%q", stored.Draft, f.disk(t, "a.txt"))
	}
}

func TestApplyAgentEditOnDivergedDocumentHoldsTheDraft(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "typed\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type", err)
	f.write(t, "a.txt", "outside\xff\n")
	observed, err := f.service.ObserveDisk(t.Context(), f.project, typed.ID, "window")
	testutil.FailErr(t, "observe", err)
	if !observed.Diverged {
		t.Fatal("fixture did not diverge")
	}

	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(observed, "typed agent\n"))
	testutil.FailErr(t, "apply", err)
	if result.Saved || !result.Document.Diverged || !result.Document.Dirty || result.Document.Draft != "typed agent\n" {
		t.Fatalf("result = %+v", result)
	}
	if f.disk(t, "a.txt") != "outside\xff\n" {
		t.Fatalf("diverged apply wrote disk: %q", f.disk(t, "a.txt"))
	}
	if len(f.recorder.records) != 0 {
		t.Fatalf("unsaved apply recorded %d ledger rows", len(f.recorder.records))
	}
}

func TestApplyAgentEditDiskRaceBecomesDivergence(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	f.write(t, "a.txt", "outside\n")

	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "agent\n"))
	testutil.FailErr(t, "apply", err)
	if result.Saved || !result.Document.Diverged || result.Document.Draft != "agent\n" || f.disk(t, "a.txt") != "outside\n" {
		t.Fatalf("result = %+v disk = %q", result, f.disk(t, "a.txt"))
	}
}

func TestDirtyDocumentsListsUnsavedDraftsOnly(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	a := f.open(t, "a.txt")
	f.open(t, "b.txt")
	_, err := f.service.ReplaceSnapshot(t.Context(), a.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: a.Revision}, Content: "a typed\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type", err)

	dirty, err := f.service.DirtyDocuments(t.Context(), f.project.ID, f.project.SourceBranch)
	testutil.FailErr(t, "dirty documents", err)
	if len(dirty) != 1 || dirty[0].Path != "a.txt" || dirty[0].Draft != "a typed\n" {
		t.Fatalf("dirty = %+v", dirty)
	}
}

func newLedgerAgentFixture(t *testing.T, files map[string]string) (agentFixture, *sourceledger.Store) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	for name, content := range files {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	store := NewStore(sqlDB)
	ledger := sourceledger.New(sqlDB, t.TempDir())
	service := New(store, ledger, ledger.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	changes := &[]Change{}
	service.SetOnChange(func(_ context.Context, c Change) { *changes = append(*changes, c) })
	return agentFixture{externalFixture: externalFixture{
		service: service, store: store, project: p, rootID: rootID, root: root, changes: changes,
	}}, ledger
}

func divergedDocumentHoldingAnAgentEdit(
	t *testing.T,
	f agentFixture,
	name, agentText string,
) *AgentEditResult {
	t.Helper()
	document := f.open(t, name)
	typed, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision}, Content: "typed\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type", err)
	f.write(t, name, "outside\xff\n")
	observed, err := f.service.ObserveDisk(t.Context(), f.project, typed.ID, "window")
	testutil.FailErr(t, "observe", err)
	if !observed.Diverged {
		t.Fatal("fixture did not diverge")
	}
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(observed, agentText))
	testutil.FailErr(t, "apply", err)
	return result
}

func TestApplyAgentEditOnDivergedDocumentRetainsTheEdit(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	result := divergedDocumentHoldingAnAgentEdit(t, f, "a.txt", "typed agent\n")

	if result.Saved || result.HeldVersionID == "" {
		t.Fatalf("result = %+v, want a held edit with a retained state", result)
	}
	if result.Document.HeldAgentVersionID != result.HeldVersionID {
		t.Fatalf("document names %q, want the retained state %q",
			result.Document.HeldAgentVersionID, result.HeldVersionID)
	}
	versions, err := ledger.History.QueryFileVersions(t.Context(), f.project.ID, result.Document.FileID, 10, 0)
	testutil.FailErr(t, "versions", err)
	var held *sourceledger.Version
	for i := range versions.Versions {
		if versions.Versions[i].ID == result.HeldVersionID {
			held = &versions.Versions[i]
		}
	}
	if held == nil {
		t.Fatalf("the held edit is not among the %d retained states", len(versions.Versions))
	}
	if held.Landing != sourceledger.LandingEditorDocument || held.Origin != api.SourceChangeOriginAgent ||
		held.Cause != sourceledger.CauseAgentEditHeld || held.ToolCallID != "call-1" {
		t.Fatalf("held state = %+v, want the agent's tool call", *held)
	}
	if held.EffectID != "" || held.Op != "" {
		t.Fatalf("held state names an effect: %+v — nothing reached the working file", *held)
	}
	restorable, err := ledger.History.ReadRestorableVersion(t.Context(), f.project.ID, result.HeldVersionID)
	testutil.FailErr(t, "read restorable", err)
	if text, ok := restorable.Text(); !ok || text != "typed agent\n" {
		t.Fatalf("restorable text = %q ok = %v, want the held edit", text, ok)
	}
	if f.disk(t, "a.txt") != "outside\xff\n" {
		t.Fatalf("held apply wrote disk: %q", f.disk(t, "a.txt"))
	}
}

func TestDiscardLeavesTheHeldEditRestorable(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	result := divergedDocumentHoldingAnAgentEdit(t, f, "a.txt", "typed agent\n")

	discarded, err := f.service.Discard(t.Context(), result.Document.ID, f.project.ID, DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: result.Document.Revision})
	testutil.FailErr(t, "discard", err)
	if discarded.HeldAgentVersionID != "" || discarded.Dirty {
		t.Fatalf("discarded document = %+v, want the draft back at its base", discarded)
	}
	restorable, err := ledger.History.ReadRestorableVersion(t.Context(), f.project.ID, result.HeldVersionID)
	testutil.FailErr(t, "read restorable", err)
	if text, ok := restorable.Text(); !ok || text != "typed agent\n" {
		t.Fatalf("discard destroyed the held edit: text = %q ok = %v", text, ok)
	}
}

func TestAgentPublicationKeepsHumanAuthorshipInOneSave(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
	document := f.open(t, "a.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", SessionID: "person-chat", Turn: 3, OperationID: uuid.NewString(), ExpectedRevision: document.Revision},
		Content:         "one typed\ntwo\n", EOL: "lf"})
	testutil.FailErr(t, "type", err)
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(typed, "one typed\ntwo agent\n"))
	testutil.FailErr(t, "apply", err)
	if !result.Saved || len(f.recorder.records) != 1 {
		t.Fatalf("shared publication = %+v, records %d", result, len(f.recorder.records))
	}
	publication := f.recorder.records[0]
	if string(publication.Before) != "one\ntwo\n" || string(publication.After) != "one typed\ntwo agent\n" || publication.TextAfter == nil {
		t.Fatalf("publication split authors into physical versions: %+v", publication)
	}
	contributions, err := f.recorder.Comparisons.DocumentContributions(t.Context(), document.ID, result.Document.Epoch, sourceledger.ContributionSelection{ThroughRevision: publication.TextAfter.Revision, Inserted: publication.TextAfter.Spans})
	testutil.FailErr(t, "read authorship", err)
	if len(contributions) != 2 || contributions[0].Origin != api.SourceChangeOriginUser || contributions[0].SessionID != "person-chat" || contributions[0].Turn != 3 ||
		contributions[1].Origin != api.SourceChangeOriginAgent || contributions[1].SessionID != "chat-1" {
		t.Fatalf("publication flattened human authorship: %+v", contributions)
	}
}

func TestApplyAgentEditOnCleanDocumentRecordsOnlyTheAgent(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")

	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "agent\n"))
	testutil.FailErr(t, "apply", err)
	if !result.Saved || result.HeldVersionID != "" {
		t.Fatalf("result = %+v, want one plain agent save", result)
	}
	if len(f.recorder.records) != 1 || f.recorder.records[0].Origin != api.SourceChangeOriginAgent {
		t.Fatalf("ledger records = %+v, want the agent's save alone", f.recorder.records)
	}
}

func TestAgentSaveJournalRecordsOrigin(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	edit := agentEdit(document, "agent\n")
	_, err := f.service.ApplyAgentEdit(t.Context(), edit)
	testutil.FailErr(t, "apply", err)
	mutation, err := f.store.Mutation(t.Context(), edit.OperationID)
	testutil.FailErr(t, "mutation", err)
	if mutation.Origin != api.SourceChangeOriginAgent || mutation.ToolCallID != "call-1" || mutation.ToolName != "edit" || mutation.Status != "complete" {
		t.Fatalf("mutation = %+v", mutation)
	}
}

// A publication that fails after the document accepted the edit is a result
// the agent can act on: the person sees the text, the file does not hold it,
// and the retained state names it. It is never a refusal of the edit.
func TestAgentPublicationFailureIsAResultNotARefusal(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	testutil.FailErr(t, "make the root unwritable", os.Chmod(f.root, 0o555))
	t.Cleanup(func() { _ = os.Chmod(f.root, 0o755) })
	if err := os.WriteFile(filepath.Join(f.root, "probe.txt"), []byte("x"), 0o644); err == nil {
		t.Skip("this environment writes into unwritable directories")
	}

	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "agent\n"))
	testutil.FailErr(t, "apply", err)
	if result.Saved || result.PublicationError == nil || result.HeldVersionID == "" {
		t.Fatalf("publication failure was not reported as a held result: %+v", result)
	}
	if result.Document.Draft != "agent\n" || !result.Document.Dirty || result.Document.HeldAgentVersionID != result.HeldVersionID {
		t.Fatalf("document did not keep the accepted edit: %+v", result.Document)
	}
	testutil.FailErr(t, "restore the root", os.Chmod(f.root, 0o755))
	if f.disk(t, "a.txt") != "base\n" {
		t.Fatalf("disk changed despite the failed publication: %q", f.disk(t, "a.txt"))
	}
	// Retrying the same operation resumes the receipt rather than applying twice.
	again, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(document, "agent\n"))
	if err != nil && !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale retry after acceptance: %v", err)
	}
	if again != nil && again.Document.Draft != "agent\n" {
		t.Fatalf("retry changed the document: %+v", again.Document)
	}
}
