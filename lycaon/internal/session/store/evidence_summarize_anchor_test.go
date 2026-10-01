package store

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"testing"
)

func summarizeAnchorJSON(anchors []map[string]any) string {
	payload := map[string]any{
		"task":  "how env config loads",
		"brief": []string{"Env vars load via cfg.FromEnv."},
	}
	if len(anchors) > 0 {
		payload["anchors"] = anchors
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestSummarizeAnchorHandlesPatched(t *testing.T) {
	dir := t.TempDir()
	store := NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "p1")
	testutil.FailErr(t, "Create", err)

	content := summarizeAnchorJSON([]map[string]any{
		{"path": "src/a.go", "line": 10, "excerpt": "func entry() {}"},
		{"path": "src/b.go", "line": 20, "excerpt": "package b"},
		{"path": "src/c.go", "line": 30, "excerpt": "import x"},
	})
	parentHandle, patched, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "summarize", map[string]any{"task": "t"}, content)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)
	if parentHandle != "summarize#4" {
		t.Fatalf("parent handle = %q want summarize#4", parentHandle)
	}

	var wire struct {
		Anchors []struct {
			Handle  string `json:"handle"`
			Path    string `json:"path"`
			Line    int    `json:"line"`
			Excerpt string `json:"excerpt"`
		} `json:"anchors"`
	}
	if err := json.Unmarshal([]byte(patched), &wire); err != nil {
		testutil.FailErr(t, "Unmarshal patched", err)
	}
	wantHandles := []string{"summarize#1", "summarize#2", "summarize#3"}
	if len(wire.Anchors) != len(wantHandles) {
		t.Fatalf("anchors = %d want %d", len(wire.Anchors), len(wantHandles))
	}
	for i, want := range wantHandles {
		if wire.Anchors[i].Handle != want {
			t.Fatalf("anchor[%d].handle = %q want %q", i, wire.Anchors[i].Handle, want)
		}
	}
}

func TestSummarizeAnchorResolvesInLedger_memory(t *testing.T) {
	dir := t.TempDir()
	store := NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "p1")
	testutil.FailErr(t, "Create", err)

	content := summarizeAnchorJSON([]map[string]any{
		{"path": "src/a.go", "line": 10, "excerpt": "func entry() {}"},
		{"path": "src/b.go", "line": 20, "excerpt": "package b"},
	})
	_, patched, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "summarize", map[string]any{"task": "t"}, content)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	rec, ok := evidence.ResolveHandle(ev, "summarize#2")
	if !ok {
		t.Fatal("missing summarize#2")
	}
	if rec.Path != "src/b.go" || rec.Body[0] != "package b" {
		t.Fatalf("rec = %+v", rec)
	}
	if !evidence.ExcerptMatchesHandle(ev, "summarize#2", 20, "package b") {
		t.Fatal("expected excerpt match for summarize#2")
	}
	_ = patched
}

func TestSummarizeAnchorResolvesInLedger_sql(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "summarize-anchor.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)

	content := summarizeAnchorJSON([]map[string]any{
		{"path": "src/a.go", "line": 10, "excerpt": "func entry() {}"},
	})
	_, _, err = store.CommitEvidenceToolResult(ctx, sess.ID, dir, "summarize", map[string]any{"task": "t"}, content)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if !evidence.ExcerptMatchesHandle(ev, "summarize#1", 10, "func entry() {}") {
		t.Fatal("expected summarize#1 excerpt match after SQL round-trip")
	}
}

func TestSummarizeNoAnchorsMintsNoneAtCommit(t *testing.T) {
	dir := t.TempDir()
	store := NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "p1")
	testutil.FailErr(t, "Create", err)

	content := summarizeAnchorJSON(nil)
	handle, patched, err := store.CommitEvidenceToolResult(ctx, sess.ID, dir, "summarize", map[string]any{"task": "t"}, content)
	testutil.FailErr(t, "CommitEvidenceToolResult", err)
	if handle != "summarize#1" {
		t.Fatalf("handle = %q want summarize#1", handle)
	}
	if patched != content {
		t.Fatalf("patched content changed without anchors")
	}

	ev, err := store.LoadLedger(ctx, sess.ID)
	testutil.FailErr(t, "LoadLedger", err)
	if _, ok := evidence.ResolveHandle(ev, "summarize#2"); ok {
		t.Fatal("expected no summarize#2 without anchors")
	}
}
