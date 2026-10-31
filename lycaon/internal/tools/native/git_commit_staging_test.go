package native

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
)

// stubAuthorship answers authorship without a store, so staging can be tested
// against a fixed ledger answer.
type stubAuthorship struct {
	paths []string
	err   error
}

func (s stubAuthorship) Record(context.Context, sourceledger.RecordInput) error { return nil }

func (s stubAuthorship) Prepare(context.Context, []sourceledger.RecordInput) (sourceledger.PreparedRecording, error) {
	return preparedCapture(func(context.Context, *sql.Tx) (sourceledger.TrackedFile, error) {
		return sourceledger.TrackedFile{}, nil
	}), nil
}

func (s stubAuthorship) SessionAuthoredPaths(_ context.Context, _, _, _ string) ([]string, error) {
	return s.paths, s.err
}

// changedPathsGit reports a fixed dirty tree; every other manager method is
// unreachable from staging resolution.
type changedPathsGit struct {
	git.GitManager
	changed []string
}

func (g changedPathsGit) ChangedPaths(context.Context, string) ([]string, error) {
	return g.changed, nil
}

func stagingCtx(paths []string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "p1",
			SessionID: "s1"},
		Source: tools.InvocationSource{ActiveRootID: "r1",
			History: tools.SourceHistory{Authorship: stubAuthorship{paths: paths}}},
	}
}

// A dirty tree carries other writers' work. The default scope is this session's
// own writes; everything else is reported as left behind, not committed.
func TestCommitStagingDefaultsToSessionAuthorship(t *testing.T) {
	gm := changedPathsGit{changed: []string{"mine.go", "peer.go", "user-notes.md"}}
	staging, err := sessionCommitStaging(
		context.Background(), gm, stagingCtx([]string{"mine.go"}), "/repo", nil)
	if err != nil {
		t.Fatalf("sessionCommitStaging: %v", err)
	}
	if len(staging.Paths) != 1 || staging.Paths[0] != "mine.go" {
		t.Fatalf("staged = %v, want only mine.go", staging.Paths)
	}
	if staging.ExcludedCount != 2 {
		t.Fatalf("ExcludedCount = %d, want 2", staging.ExcludedCount)
	}
	for _, p := range staging.Excluded {
		if p == "mine.go" {
			t.Fatal("a staged path must not also report as uncommitted")
		}
	}
}

// Authored-but-clean paths were already committed in this session, so they add
// no no-op entries to the staged list.
func TestCommitStagingDropsAuthoredPathsWithoutChanges(t *testing.T) {
	gm := changedPathsGit{changed: []string{"second.go"}}
	staging, err := sessionCommitStaging(
		context.Background(), gm,
		stagingCtx([]string{"first.go", "second.go"}), "/repo", nil)
	if err != nil {
		t.Fatalf("sessionCommitStaging: %v", err)
	}
	if len(staging.Paths) != 1 || staging.Paths[0] != "second.go" {
		t.Fatalf("staged = %v, want only second.go", staging.Paths)
	}
}

// Unattributed dirty files require explicit path selection.
func TestCommitStagingRejectsWithoutSessionAuthorship(t *testing.T) {
	gm := changedPathsGit{changed: []string{"peer.go"}}
	_, err := sessionCommitStaging(
		context.Background(), gm, stagingCtx(nil), "/repo", nil)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("err = %v, want a structured reject", err)
	}
	if reject.Code != "GIT_COMMIT_NO_SESSION_AUTHORSHIP" {
		t.Fatalf("Code = %q, want GIT_COMMIT_NO_SESSION_AUTHORSHIP", reject.Code)
	}
}

// A ledger that cannot answer authorship is not permission to stage everything.
func TestCommitStagingRejectsWhenLedgerCannotAnswer(t *testing.T) {
	gm := changedPathsGit{changed: []string{"peer.go"}}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "p1",
			SessionID: "s1"},
		Source: tools.InvocationSource{ActiveRootID: "r1"},
	}
	_, err := sessionCommitStaging(context.Background(), gm, tctx, "/repo", nil)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_COMMIT_NO_SESSION_AUTHORSHIP" {
		t.Fatalf("err = %v, want GIT_COMMIT_NO_SESSION_AUTHORSHIP", err)
	}
}

// Paths the session staged itself join its authorship; unrelated changes stay out.
func TestCommitStagingIncludesSessionStagedPaths(t *testing.T) {
	gm := changedPathsGit{changed: []string{"generated.go", "peer.go"}}
	staging, err := sessionCommitStaging(context.Background(), gm, stagingCtx(nil), "/repo", []string{"generated.go"})
	if err != nil {
		t.Fatalf("sessionCommitStaging: %v", err)
	}
	if len(staging.Paths) != 1 || staging.Paths[0] != "generated.go" || staging.ExcludedCount != 1 {
		t.Fatalf("staging = %+v, want generated.go with peer.go excluded", staging)
	}
}
