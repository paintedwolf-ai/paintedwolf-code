package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMergeReceiptsSurviveFileObservationAndStoreReload(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			var store interface {
				Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
				CommitEvidenceToolResult(context.Context, string, string, string, map[string]any, string) (string, string, error)
				LoadLedger(context.Context, string) (evidence.Ledger, error)
			} = NewMemory()
			if backend == "sql" {
				db := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
				testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, dir)
				store = NewSQL(db)
			}
			sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			commit := func(tool, path, body string) string {
				t.Helper()
				handle, _, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, tool, map[string]any{"path": path}, body)
				testutil.FailErr(t, "record observation", err)
				if handle == "" {
					t.Fatalf("%s did not produce evidence", tool)
				}
				return handle
			}
			merge := commit("command", "", `{"exit_code":0,"tail":"Fast-forward\n content/sdk/agents.md | 10 +\n data/og_cards.yaml | 8 +\n"}`)
			commit("read", "content/sdk/agents.md", `{"path":"content/sdk/agents.md","content":"1|original SDK content","offset":1,"end_line":1}`)
			status := commit("git_status", "", `{"available":true,"files":[{"path":"data/og_cards.yaml","status":"UU"}]}`)
			conflict := commit("command", "", `{"exit_code":1,"tail":"CONFLICT (add/add): Merge conflict in data/og_cards.yaml"}`)
			read := commit("read", "data/og_cards.yaml", `{"path":"data/og_cards.yaml","content":"1|<<<<<<< HEAD","offset":1,"end_line":1}`)
			write := commit("write", "data/og_cards.yaml", `{"ok":true}`)
			gitCommit := commit("git_commit", "", `{"available":true,"hash":"dfc9c6bb","paths":["data/og_cards.yaml"]}`)
			if sqlStore, ok := store.(*SQL); ok {
				store = NewSQL(sqlStore.db)
			}
			ev, err := store.LoadLedger(ctx, sess.ID)
			testutil.FailErr(t, "reload ledger", err)
			for _, handle := range []string{merge, status, conflict, gitCommit} {
				rec, ok := evidence.ResolveHandle(ev, handle)
				if !ok || rec.SupersededBy != "" {
					t.Fatalf("receipt lost its identity: %+v", rec)
				}
			}
			if ev.Handles[read].SupersededBy != write || !strings.Contains(strings.Join(ev.Handles[read].Body, "\n"), "<<<<<<< HEAD") {
				t.Fatal("historical file observation not retained")
			}
			report := guidance.CoordinatorCompletionReport{Synthesis: "Merged the branch and resolved its conflict."}
			for _, handle := range []string{merge, status, conflict, read, gitCommit} {
				report.CitedEvidence = append(report.CitedEvidence, guidance.CoordinatorCitedEvidence{Evidence: handle})
			}
			eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{ProjectDir: dir}, "implement_investigate", report, guidance.CloseoutEvidence{Ledger: ev})
			if eval.Code != "" {
				t.Fatalf("historical receipts rejected: %+v", eval)
			}
		})
	}
}

func TestHistoricalEvidenceRetainsUntrustedExposureAfterReload(t *testing.T) {
	dir := t.TempDir()
	db := testdbfixture.OpenPath(t, filepath.Join(dir, "ledger.db"))
	testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, dir)
	store := NewSQL(db)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "record external observation", store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url", URL: "https://example.org/source"}))
	untrusted, err := store.SessionUntrustedContentResult(ctx, sess.ID)
	testutil.FailErr(t, "read initial exposure", err)
	if !untrusted {
		t.Fatal("retrieval did not record untrusted-content exposure")
	}
	testutil.FailErr(t, "replace path binding", store.MarkSuperseded(ctx, sess.ID, "web#1", "read#1"))
	loaded := NewSQL(db)
	untrusted, err = loaded.SessionUntrustedContentResult(ctx, sess.ID)
	testutil.FailErr(t, "reload exposure", err)
	if !untrusted {
		t.Fatal("supersession erased historical untrusted-content exposure")
	}
}
