package search

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectToolCallsSurfacesNameAndArgs(t *testing.T) {
	msg := api.Message{
		ID:        "asst-1",
		Role:      api.MessageRoleAssistant,
		CreatedAt: time.Now().UTC(),
		ToolCalls: []api.ToolCall{
			{ID: "call-0", Name: "update_progress", Args: map[string]any{"content": "- [ ] build chess engine"}},
			{ID: "call-1", Name: "list_dir", Args: map[string]any{"path": "."}},
		},
	}
	rows := ProjectToolCalls("proj-a", "sess-a", msg)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for i, r := range rows {
		if r.HitKind != HitKindTool || r.Source != SourceMessage {
			t.Fatalf("kind/source = %q/%q", r.HitKind, r.Source)
		}
		if r.MessageID != "asst-1" {
			t.Fatalf("message id = %q, want asst-1", r.MessageID)
		}
		// SourceRef uses the tool-call anchor.
		if want := msg.ToolCalls[i].ID; r.SourceRef != want {
			t.Fatalf("source_ref = %q, want call id %q", r.SourceRef, want)
		}
	}
	if rows[0].Tool != "update_progress" || rows[0].Handle != "" || !strings.Contains(rows[0].Snippet, "build chess engine") {
		t.Fatalf("tool call 0 = %+v", rows[0])
	}
}

func TestProjectToolCallsAnchorStructuredPathArg(t *testing.T) {
	msg := api.Message{
		ID:        "asst-2",
		Role:      api.MessageRoleAssistant,
		CreatedAt: time.Now().UTC(),
		ToolCalls: []api.ToolCall{
			{ID: "call-0", Name: "read", Args: map[string]any{"path": "internal/store/store.go"}},
			{ID: "call-1", Name: "command", Args: map[string]any{"command": "go test ./..."}},
			{ID: "call-2", Name: "read", Args: map[string]any{"path": 42}},
		},
	}
	rows := ProjectToolCalls("proj-a", "sess-a", msg)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// Structured paths anchor rows to files.
	if rows[0].Path != "internal/store/store.go" {
		t.Fatalf("read path = %q", rows[0].Path)
	}
	// Missing or non-string paths remain unanchored.
	if rows[1].Path != "" || rows[2].Path != "" {
		t.Fatalf("non-path rows anchored: %q / %q", rows[1].Path, rows[2].Path)
	}
}

func TestProjectedToolCallPathRoundTripsThroughStoreSearch(t *testing.T) {
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))

	ctx := context.Background()
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-tool", projectID, dir)

	rows := ProjectToolCalls(projectID, "sess-tool", api.Message{
		ID:        "asst-tool",
		Role:      api.MessageRoleAssistant,
		CreatedAt: time.Now().UTC(),
		ToolCalls: []api.ToolCall{
			{ID: "call-read", Name: "read", Args: map[string]any{"path": "internal/store/store.go"}},
		},
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "BeginTx", err)
	testutil.FailErr(t, "UpsertRows", NewStore().UpsertRows(ctx, tx, rows))
	testutil.FailErr(t, "Commit", tx.Commit())

	plan, err := CompileQuery("kind:tool", CompileContext{
		OriginProjectID:    projectID,
		RootsForProject:    func(string) ([]CodeRoot, error) { return nil, nil },
		AttachedProjectIDs: func() ([]string, error) { return []string{projectID}, nil },
	})
	testutil.FailErr(t, "CompileQuery", err)
	report, err := NewStoreExecutor(sqlDB).Run(ctx, PlanLeg{
		Executor: ExecutorStore,
		Cap:      SearchExecutorProbeHits,
		Store:    plan.Store,
	})
	testutil.FailErr(t, "StoreExecutor.Run", err)
	hits := report.Hits
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].Path != "internal/store/store.go" {
		t.Fatalf("hit path = %q, want the structured path arg", hits[0].Path)
	}
	if hits[0].SourceRef != "call-read" {
		t.Fatalf("source_ref = %q, want the chicklet anchor", hits[0].SourceRef)
	}
}

func TestProjectToolCallsSkipsNonAssistantAndEmpty(t *testing.T) {
	if rows := ProjectToolCalls("p", "s", api.Message{ID: "u", Role: api.MessageRoleUser, Content: "hi"}); len(rows) != 0 {
		t.Fatalf("user message produced %d tool-call rows", len(rows))
	}
	if rows := ProjectToolCalls("p", "s", api.Message{ID: "a", Role: api.MessageRoleAssistant}); len(rows) != 0 {
		t.Fatalf("assistant with no tool calls produced %d rows", len(rows))
	}
}

func TestProjectToolMessageIndexesEgressHosts(t *testing.T) {
	msg := api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		CreatedAt:  time.Now().UTC(),
		ToolResult: &api.ToolResult{ToolCallID: "call-9"},
		Content: `{"command":"npm install","exit_code":0,"ok":true,"network":` +
			`[{"host":"registry.npmjs.org","allowed":true},{"host":"evil.test","allowed":false}]}`,
	}
	var net []IndexRow
	for _, r := range ProjectToolMessage("proj-a", "sess-a", msg, "command") {
		if r.HitKind == HitKindNetwork {
			net = append(net, r)
		}
	}
	if len(net) != 2 {
		t.Fatalf("expected 2 network egress rows, got %d", len(net))
	}
	// Egress rows separate reveal and projection identities.
	if net[0].SourceRef != "call-9" || net[0].MessageID != "tool-1" {
		t.Fatalf("egress identity = %+v", net[0])
	}
	if net[0].URL != "registry.npmjs.org" || !strings.Contains(net[0].Snippet, "connected to") {
		t.Fatalf("allowed host row = %+v", net[0])
	}
	if net[1].URL != "evil.test" || !strings.Contains(net[1].Snippet, "blocked from") {
		t.Fatalf("blocked host row = %+v", net[1])
	}
}

func TestProjectToolMessageNonWebResultIndexed(t *testing.T) {
	msg := api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    "npm test\n5 passing",
		ToolResult: &api.ToolResult{ToolCallID: "call-7"},
		CreatedAt:  time.Now().UTC(),
	}
	rows := ProjectToolMessage("proj-a", "sess-a", msg, "command")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.HitKind != HitKindTool || r.Source != SourceTool || r.Tool != "command" || r.Handle != "" {
		t.Fatalf("non-web tool result row = %+v", r)
	}
	// SourceRef and MessageID keep distinct identities.
	if r.SourceRef != "call-7" || r.MessageID != "tool-1" {
		t.Fatalf("tool result identity = %+v", r)
	}
	if !strings.Contains(r.Snippet, "5 passing") {
		t.Fatalf("snippet = %q", r.Snippet)
	}
	if r.Shape != "raw" {
		t.Fatalf("shape = %q, want raw", r.Shape)
	}
}

func TestProjectToolMessageDoesNotInferToolIdentity(t *testing.T) {
	msg := api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    `{"query":"auth","results":[{"url":"https://example.test"}]}`,
		ToolResult: &api.ToolResult{ToolCallID: "call-1"},
	}
	rows := ProjectToolMessage("proj-a", "sess-a", msg, "")
	if len(rows) != 1 || rows[0].HitKind != HitKindTool || rows[0].Tool != "" || rows[0].Handle != "" {
		t.Fatalf("rows = %+v", rows)
	}
}

// The handle a tool result minted in the evidence ledger is the row's handle,
// so `handle:command#2` from a compaction banner resolves to this row and the
// recorded body behind it.
func TestProjectToolMessageIndexesLedgerHandles(t *testing.T) {
	msg := api.Message{
		ID:              "tool-9",
		Role:            api.MessageRoleTool,
		Content:         "[command#2]\n{\"tail\":\"M a.go\"}",
		ToolResult:      &api.ToolResult{ToolCallID: "call-9"},
		EvidenceHandles: []string{"command#2", "net#1"},
		CreatedAt:       time.Now().UTC(),
	}
	rows := ProjectToolMessage("proj-a", "sess-a", msg, "command")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want one per minted handle", len(rows))
	}
	if rows[0].Handle != "command#2" || rows[1].Handle != "net#1" {
		t.Fatalf("handles = %q, %q", rows[0].Handle, rows[1].Handle)
	}
	for _, r := range rows {
		if r.Tool != "command" || r.HitKind != HitKindTool || r.SourceRef != "call-9" {
			t.Fatalf("row = %+v", r)
		}
	}
	if rows[0].ID == rows[1].ID {
		t.Fatal("rows for distinct handles must not share an id")
	}
}

func TestProjectToolMessageDoesNotSubstituteRevealIdentity(t *testing.T) {
	msg := api.Message{
		ID:      "tool-1",
		Role:    api.MessageRoleTool,
		Content: "result",
	}
	rows := ProjectToolMessage("proj-a", "sess-a", msg, "read")
	if len(rows) != 1 || rows[0].SourceRef != "" {
		t.Fatalf("rows = %+v", rows)
	}
}
