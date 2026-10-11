package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

type selectiveRecoveryRecorder struct {
	captureRecorder
	failedPath string
}

func (r *selectiveRecoveryRecorder) RecordTx(_ context.Context, _ *sql.Tx, in sourceledger.RecordInput) error {
	if in.Path == r.failedPath {
		return errors.New("injected ledger outage")
	}
	return nil
}

func (r *selectiveRecoveryRecorder) RecordFileTx(ctx context.Context, tx *sql.Tx, in sourceledger.RecordInput) (sourceledger.TrackedFile, error) {
	return sourceledger.TrackedFile{}, r.RecordTx(ctx, tx, in)
}

func TestRecoverySettlesOtherDocumentsAfterOneLedgerFailure(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, database, projectID, rootID, root)
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	recorder := &selectiveRecoveryRecorder{captureRecorder: captureRecorder{Store: sourceledger.New(database, "")}}
	store := NewStore(database)
	service := New(store, recorder, recorder.History, fixedRoots{p: p})
	closeServiceAtCleanup(t, service)
	operations := make([]string, 0, 2)
	for _, name := range []string{"blocked.txt", "available.txt"} {
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, name), []byte("base\n"), 0o600))
		doc, err := service.Open(t.Context(), p, name, rootID, "", "window", nil)
		testutil.FailErr(t, "open document", err)
		doc, err = service.ReplaceSnapshot(t.Context(), doc.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "saved\n", EOL: "lf", MixedEOL: false})
		testutil.FailErr(t, "update draft", err)
		recorder.failedPath = name
		operation := uuid.NewString()
		operations = append(operations, operation)
		if _, err := service.Save(t.Context(), p, doc.ID, "window", operation, "", 0, doc.Revision); err == nil {
			t.Fatal("save succeeded during ledger failure")
		}
	}
	recorder.failedPath = "blocked.txt"
	if err := service.Recover(t.Context()); err == nil {
		t.Fatal("recovery hid the remaining ledger failure")
	}
	for i, want := range []string{"file_applied", "complete"} {
		mutation, err := store.Mutation(t.Context(), operations[i])
		testutil.FailErr(t, "read recovered mutation", err)
		if mutation.Status != want {
			t.Fatalf("mutation %d status = %s, want %s", i, mutation.Status, want)
		}
	}
}

func TestRecoveryPreservesDraftAndSettlesUnavailableTarget(t *testing.T) {
	for _, scenario := range []string{"newer_draft", "missing_file", "crlf_snapshot", "applied_target_removed"} {
		t.Run(scenario, func(t *testing.T) {
			database := testdbfixture.Open(t, "store.db")
			root := t.TempDir()
			projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
			testdbseed.InsertProjectRootWithID(t, database, projectID, rootID, root)
			path := filepath.Join(root, "a.txt")
			testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte("base\n"), 0o600))
			p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
			recorder := &captureRecorder{Store: sourceledger.New(database, ""), err: errors.New("injected ledger outage")}
			store := NewStore(database)
			service := New(store, recorder, recorder.History, fixedRoots{p: p})
			closeServiceAtCleanup(t, service)
			doc, err := service.Open(t.Context(), p, "a.txt", rootID, "", "window", nil)
			testutil.FailErr(t, "open document", err)
			firstDraft, firstEOL := "saved old draft\n", "lf"
			if scenario == "crlf_snapshot" {
				firstDraft, firstEOL = "saved old draft", "crlf"
			}
			doc, err = service.ReplaceSnapshot(t.Context(), doc.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: firstDraft, EOL: firstEOL, MixedEOL: false})
			testutil.FailErr(t, "update first draft", err)
			operationID := uuid.NewString()
			if _, err = service.Save(t.Context(), p, doc.ID, "window", operationID, "", 0, doc.Revision); err == nil {
				t.Fatal("injected save failure did not occur")
			}
			mutation, err := store.Mutation(t.Context(), operationID)
			testutil.FailErr(t, "read pending mutation", err)
			recorder.err = nil
			if scenario == "applied_target_removed" {
				testutil.FailErr(t, "remove applied target", os.Remove(path))
				testutil.FailErr(t, "recover applied target", service.Recover(t.Context()))
				recovered, err := store.Get(t.Context(), doc.ID)
				testutil.FailErr(t, "read diverged document", err)
				if !recovered.Diverged || recovered.Draft != firstDraft || recovered.BaseContent != firstDraft {
					t.Fatalf("lost applied snapshot or divergence: %+v", recovered)
				}
				return
			}
			if scenario == "missing_file" {
				mutation.Status = "prepared"
				testutil.FailErr(t, "simulate pre-journal crash", store.UpdateMutation(t.Context(), mutation))
				testutil.FailErr(t, "remove disposable fixture", os.Remove(path))
				testutil.FailErr(t, "recover missing target", service.Recover(t.Context()))
				settled, err := store.Mutation(t.Context(), operationID)
				testutil.FailErr(t, "read settled mutation", err)
				recovered, err := store.Get(t.Context(), doc.ID)
				testutil.FailErr(t, "read preserved draft", err)
				if settled.Status != "conflict" || settled.Error == "" || !recovered.Diverged || !recovered.Dirty || recovered.Draft != "saved old draft\n" {
					t.Fatalf("unsettled recovery: mutation=%+v document=%+v", settled, recovered)
				}
				return
			}
			doc, err = service.ReplaceSnapshot(t.Context(), doc.ID, projectID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "newest unsaved draft\n", EOL: "lf", MixedEOL: false})
			testutil.FailErr(t, "update newer draft", err)
			testutil.FailErr(t, "recover earlier save", service.recoverMutation(t.Context(), p, mutation))
			recovered, err := store.Get(t.Context(), doc.ID)
			testutil.FailErr(t, "read recovered document", err)
			raw, err := os.ReadFile(path)
			testutil.FailErr(t, "read disk", err)
			if !recovered.Dirty || recovered.Draft != "newest unsaved draft\n" || recovered.BaseContent != string(raw) || string(raw) != firstDraft || recovered.BaseEOL != firstEOL || recovered.EOL != "lf" {
				t.Errorf("recovery lost newer draft or immutable base: draft=%q base=%q disk=%q dirty=%v", recovered.Draft, recovered.BaseContent, raw, recovered.Dirty)
			}
		})
	}
}
