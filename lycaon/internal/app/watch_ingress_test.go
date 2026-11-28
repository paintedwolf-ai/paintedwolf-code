package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

type watchIngressProbe struct {
	store   *watch.Store
	owner   watch.Owner
	matched map[string]bool
}

func (p watchIngressProbe) register(t *testing.T, cond awaitstore.Condition, auth watch.Authority) watch.RegisterReceipt {
	t.Helper()
	receipt, err := p.store.Register(t.Context(), watch.RegisterRequest{
		Owner: p.owner, InvocationID: "ingress-" + cond.Kind,
		Conditions: []awaitstore.Condition{cond}, Authority: auth,
	})
	testutil.FailErr(t, "register ingress watch", err)
	if receipt.PendingMatches != 0 {
		t.Fatalf("%s was already matched before its event", cond.Kind)
	}
	return receipt
}

func (p watchIngressProbe) expectMatch(t *testing.T, receipt watch.RegisterReceipt, kind string) {
	t.Helper()
	view, err := p.store.Inspect(t.Context(), p.owner, receipt.Handle)
	testutil.FailErr(t, "read durable watch state", err)
	if view.PendingMatches != 1 || view.LatestMatch == nil ||
		view.LatestMatch.WatchID != receipt.WatchID || view.LatestMatch.SourceKind != kind || view.LatestMatch.OccurrenceID == "" {
		t.Errorf("%s watch %s: want one persisted match from its producer, got %+v", kind, receipt.WatchID, view)
	}
	p.matched[kind] = true
}

// Discover producer coverage from the actual registered watch schema. Polling
// and timers are owned by the watch scheduler and exercised with its fake clock
// in WW03, WW05 and the source integration tests, without an app startup hook.
func (p watchIngressProbe) checkInventory(t *testing.T, built *ServeApp) {
	t.Helper()
	meta, ok := built.ToolRegistry.Meta("watch")
	if !ok {
		t.Fatal("watch tool not registered by Build")
	}
	properties, _ := meta.ArgsSchema["properties"].(map[string]any)
	conditions, _ := properties["conditions"].(map[string]any)
	items, _ := conditions["items"].(map[string]any)
	fields, _ := items["properties"].(map[string]any)
	kind, _ := fields["kind"].(map[string]any)
	vocabulary, _ := kind["enum"].([]any)
	if len(vocabulary) == 0 {
		t.Fatal("registered watch schema declares no condition kinds")
	}
	declared := make(map[string]bool, len(vocabulary))
	for _, value := range vocabulary {
		name, ok := value.(string)
		if !ok {
			t.Errorf("invalid condition kind %v", value)
			continue
		}
		declared[name] = true
		switch name {
		case "timer", "http_ready", "port_ready":
			continue
		}
		if !p.matched[name] {
			t.Errorf("%s: missing durable match witness through startup wiring", name)
		}
	}
	for name := range p.matched {
		if !declared[name] {
			t.Errorf("%s: matched by a producer but missing from the registered schema", name)
		}
	}
}

func assertBuiltWatchWorkspaceProducers(t *testing.T, probe watchIngressProbe, root string) {
	t.Helper()
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(root)
	testutil.FailErr(t, "resolve workspace root", err)
	roots := []projectroot.RootRef{{ID: "repo", Label: "repo", Path: root, IsPrimary: true}}
	auth := watch.Authority{ProjectDir: root, Roots: []watch.WatchRootGrant{{Label: "repo", RootID: "repo", Path: root}}}

	path := filepath.Join(root, "watch-ingress.txt")
	testutil.FailErr(t, "seed file", os.WriteFile(path, []byte("before\n"), 0o600))
	file := probe.register(t, awaitstore.Condition{Kind: "file_change", Root: "repo", Path: "watch-ingress.txt"}, auth)
	testutil.FailErr(t, "write changed content", os.WriteFile(path, []byte("after\n"), 0o600))
	testutil.FailErr(t, "publish workspace signal", sourcefeed.EmitProjectSignal(ctx, probe.owner.ProjectID, roots))
	probe.expectMatch(t, file, "file_change")

	// The ref producer reads loose refs directly. Seed two valid object names
	// without requiring the bundled Git executable in the short test gate.
	const branch = "refs/heads/main"
	gitDir := filepath.Join(root, ".git")
	refPath := filepath.Join(gitDir, branch)
	testutil.FailErr(t, "create loose refs directory", os.MkdirAll(filepath.Dir(refPath), 0o700))
	testutil.FailErr(t, "seed Git HEAD", os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: "+branch+"\n"), 0o600))
	testutil.FailErr(t, "seed Git ref", os.WriteFile(refPath, []byte(strings.Repeat("a", 40)+"\n"), 0o600))
	ref := probe.register(t, awaitstore.Condition{Kind: "git_ref", Root: "repo", Ref: branch}, auth)
	testutil.FailErr(t, "move Git ref", os.WriteFile(refPath, []byte(strings.Repeat("b", 40)+"\n"), 0o600))
	repochange.Notify(ctx, repochange.Event{Kind: repochange.HeadMoved, ProjectDir: root})
	probe.expectMatch(t, ref, "git_ref")
}

func assertBuiltWatchScanProducer(t *testing.T, built *ServeApp, probe watchIngressProbe, root string) {
	t.Helper()
	ctx := t.Context()
	scanRow := api.CodeScan{
		ID: "watch-ingress-scan", SessionID: probe.owner.SessionID, CanonicalPath: root,
		Categories: []api.ScanCategory{api.ScanCategorySecurity}, Status: api.CodeScanStatusPending,
		CreatedAt: time.Now().UTC(), HeadSHA: "test-head", Trigger: api.ScanTriggerManual,
	}
	testutil.FailErr(t, "seed requested scan", scan.NewSQLStore(built.DB).Runs.Insert(ctx, scanRow, nil, ""))
	receipt := probe.register(t, awaitstore.Condition{Kind: "scan_done"}, watch.Authority{})
	scanRow.Status = api.CodeScanStatusComplete
	built.Sessions.Manager.Coordinator.Scans.Finished(ctx, scanRow)
	probe.expectMatch(t, receipt, "scan_done")
}

func assertBuiltWatchWorkerProducers(t *testing.T, built *ServeApp, probe watchIngressProbe) {
	t.Helper()
	ctx := t.Context()
	const child, job = "watch-ingress-child", "watch-ingress-job"
	testdbseed.InsertChildSession(t, built.DB, child, probe.owner.SessionID)
	_, err := built.DB.ExecContext(ctx, `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, ?, ?, 'implementer', 'running', 'work', 'work', '2026-05-01T10:00:00Z')`,
		job, probe.owner.ProjectID, probe.owner.SessionID, child)
	testutil.FailErr(t, "seed running worker", err)
	receipt := probe.register(t, awaitstore.Condition{Kind: "worker_decision_needed"}, watch.Authority{})
	testutil.FailErr(t, "publish worker decision", built.Sessions.Decisions.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: child, WorkerID: job, Question: "Choose an option", Options: []string{"continue", "stop"},
	}))
	probe.expectMatch(t, receipt, "worker_decision_needed")

	notice := probe.register(t, awaitstore.Condition{Kind: "next_worker_done", Handles: []string{job}}, watch.Authority{})
	built.Sessions.Manager.Coordinator.Workers.RoundNotice(ctx, probe.owner.SessionID, kick.WorkerRoundNoticeFacts{JobID: job})
	probe.expectMatch(t, notice, "next_worker_done")

	idle := probe.register(t, awaitstore.Condition{Kind: "all_workers_idle"}, watch.Authority{})
	overlay := probe.register(t, awaitstore.Condition{Kind: "overlay_promote_pending"}, watch.Authority{})
	_, err = built.DB.ExecContext(ctx, `UPDATE worker_jobs
		SET status = 'complete', merge_status = 'pending', workspace_relpath = ?, scope_json = '{"mode":"write"}'
		WHERE id = ?`, enginepaths.WorkerBranchesDirName+"/"+job, job)
	testutil.FailErr(t, "publish worker outcome state", err)
	testutil.FailErr(t, "acknowledge worker outcome", built.Workers.Queue.MarkOutcomeDelivered(ctx, job))
	built.Sessions.Manager.Coordinator.Workers.Terminal(ctx, probe.owner.SessionID, job)
	probe.expectMatch(t, idle, "all_workers_idle")
	probe.expectMatch(t, overlay, "overlay_promote_pending")

	_, err = built.DB.ExecContext(ctx, "UPDATE worker_jobs SET status = 'failed', merge_status = NULL WHERE id = ?", job)
	testutil.FailErr(t, "seed retained worker failure", err)
	failure, err := probe.store.Register(ctx, watch.RegisterRequest{
		Owner: probe.owner, InvocationID: "ingress-retained-worker-failure",
		Conditions: []awaitstore.Condition{{Kind: "worker_failed", Handles: []string{job}}},
	})
	testutil.FailErr(t, "register against retained failure", err)
	probe.expectMatch(t, failure, "worker_failed")
}
