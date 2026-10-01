package session_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestSessionUntrustedContentFreshIsClean(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)
	if store.SessionUntrustedContent(sess.ID) {
		t.Fatal("fresh session must not be untrusted")
	}
	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.UntrustedContent {
		t.Fatal("Get wire field must be false for fresh session")
	}
}

func TestSessionUntrustedContentWebAndMCP(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	if err := store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "read#1", Kind: "file", Shape: evidence.ShapeFileRegion, SourceTool: "read",
	}); err != nil {
		testutil.FailErr(t, "upsert read", err)
	}
	if store.SessionUntrustedContent(sess.ID) {
		t.Fatal("read must not mark untrusted")
	}

	if err := store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: "https://example.com/a",
	}); err != nil {
		testutil.FailErr(t, "upsert fetch_url", err)
	}
	if !store.SessionUntrustedContent(sess.ID) {
		t.Fatal("fetch_url must mark untrusted")
	}

	sess2, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create 2", err)
	if err := store.UpsertEvidenceRecord(ctx, sess2.ID, evidence.Record{
		Handle: "mcp#1", Kind: "mcp", Shape: evidence.ShapeOpaque, SourceTool: "mcp_docs_get",
	}); err != nil {
		testutil.FailErr(t, "upsert mcp", err)
	}
	if !store.SessionUntrustedContent(sess2.ID) {
		t.Fatal("any MCP tool must mark untrusted")
	}

	sess3, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create 3", err)
	if err := store.UpsertEvidenceRecord(ctx, sess3.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "web_search",
	}); err != nil {
		testutil.FailErr(t, "upsert web_search", err)
	}
	if !store.SessionUntrustedContent(sess3.ID) {
		t.Fatal("web_search must mark untrusted")
	}
}

func TestSessionUntrustedContentSpawnInheritance(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	if err := store.UpsertEvidenceRecord(ctx, parent.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: "https://example.com",
	}); err != nil {
		testutil.FailErr(t, "upsert parent", err)
	}

	child, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "do work"})
	testutil.FailErr(t, "SpawnChild", err)
	if !child.UntrustedContent {
		t.Fatal("spawned child must be born untrusted when parent is")
	}
	if !store.SessionUntrustedContent(child.ID) {
		t.Fatal("child SessionUntrustedContent must be true after inherit seed")
	}

	clean, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create clean parent", err)
	cleanChild, err := mgr.SpawnChild(ctx, clean.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "do work"})
	testutil.FailErr(t, "SpawnChild clean", err)
	if cleanChild.UntrustedContent || store.SessionUntrustedContent(cleanChild.ID) {
		t.Fatal("child of trusted parent must stay clean until its own untrusted ingest")
	}
}

func TestMergeWorkerUntrustedIntoParent(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: orchestration.ProfileWebResearcher})
	testutil.FailErr(t, "create child", err)

	childURL := "https://docs.example.com/page"
	if err := store.UpsertEvidenceRecord(ctx, child.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: childURL, Fidelity: evidence.FidelityStructured,
	}); err != nil {
		testutil.FailErr(t, "upsert child url", err)
	}

	if _, err := mgr.AppendWorkerSummary(ctx, parent.ID, session.WorkerSummaryInput{
		Summary:        "research done",
		JobID:          "job-web-1",
		ChildSessionID: child.ID,
		AgentType:      orchestration.ProfileWebResearcher,
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	if !store.SessionUntrustedContent(parent.ID) {
		t.Fatal("web-researcher summary ingest must mark parent untrusted")
	}
	parentEv, err := store.LoadLedger(ctx, parent.ID)
	testutil.FailErr(t, "LoadLedger parent", err)
	urls := evidence.ObservedURLsSorted(parentEv)
	found := false
	for _, u := range urls {
		if u == childURL {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("parent ledger must include merged child URL; got %v", urls)
	}
}

func TestMergeWorkerUntrustedIntoParentUsesChildEvidenceNotAgentName(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "custom-all-tools"})
	testutil.FailErr(t, "create child", err)
	if err := store.UpsertEvidenceRecord(ctx, child.ID, evidence.Record{
		Handle: "web#custom", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: "https://example.com/custom", Fidelity: evidence.FidelityStructured,
	}); err != nil {
		testutil.FailErr(t, "upsert child url", err)
	}

	if _, err := mgr.AppendWorkerSummary(ctx, parent.ID, session.WorkerSummaryInput{
		Summary: "done", JobID: "job-custom", ChildSessionID: child.ID, AgentType: "custom-all-tools",
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	if !store.SessionUntrustedContent(parent.ID) {
		t.Fatal("custom worker with untrusted evidence must mark parent untrusted")
	}

	cleanParent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create clean parent", err)
	cleanChild, err := store.CreateChild(ctx, cleanParent, api.SpawnChildRequest{AgentType: orchestration.ProfileWebResearcher})
	testutil.FailErr(t, "create clean child", err)
	if _, err := mgr.AppendWorkerSummary(ctx, cleanParent.ID, session.WorkerSummaryInput{
		Summary: "done", JobID: "job-clean", ChildSessionID: cleanChild.ID, AgentType: orchestration.ProfileWebResearcher,
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary clean", err)
	}
	if store.SessionUntrustedContent(cleanParent.ID) {
		t.Fatal("agent name without child evidence must not mark parent untrusted")
	}
}

func TestSessionUntrustedContentSQLRebuild(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "untrusted.db")
	hot := store.NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := hot.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	if err := hot.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "web_search",
	}); err != nil {
		testutil.FailErr(t, "upsert", err)
	}
	// Cold cache path: new store handle over same DB rebuilds from rows.
	cold := store.NewSQL(sqlDB)
	if !cold.SessionUntrustedContent(sess.ID) {
		t.Fatal("SQL rebuild-from-records must report untrusted")
	}
	got, err := cold.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if !got.UntrustedContent {
		t.Fatal("Get must hydrate untrusted_content after rebuild")
	}
}
