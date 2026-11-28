// Package wea_test implements the TB06 (Attribution, restore and rewind)
// in-process verification suite covering case families E04, E05, E06, E07, and E09.
// This file is strictly test-only (*_test.go) and is never linked into any shipped binary.
package wea_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

func tb06SHA256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func tb06SHA256File(t *testing.T, path string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return tb06SHA256Bytes(raw), true
}

func tb06HashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := make(map[string]string)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		hashes[filepath.ToSlash(rel)] = tb06SHA256Bytes(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("hash tree %s: %v", root, err)
	}
	return hashes
}

func tb06EqualHashMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func tb06TableCountsRO(t *testing.T, ctx context.Context, dbPath string, tables []string) map[string]int {
	t.Helper()
	ro, err := db.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.OpenReadOnly(%s): %v", dbPath, err)
	}
	defer ro.Close()
	out := make(map[string]int, len(tables))
	for _, tbl := range tables {
		var n int
		if err := ro.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, tbl)).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		out[tbl] = n
	}
	return out
}

func tb06EqualCountMaps(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func tb06RunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
	}
	return string(out)
}

func tb06InitGitRepo(t *testing.T, dir string) {
	t.Helper()
	tb06RunGit(t, dir, "init", "-q")
	tb06RunGit(t, dir, "config", "user.name", "WEA TB06")
	tb06RunGit(t, dir, "config", "user.email", "wea-tb06@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("tb06 fixture\n"), 0o644); err != nil {
		t.Fatalf("seed README.md: %v", err)
	}
	tb06RunGit(t, dir, "add", ".")
	tb06RunGit(t, dir, "commit", "-q", "-m", "initial")
}

type tb06Env struct {
	dbPath     string
	store      *db.Store
	contentDir string
	dataDir    string
	projectDir string
	projectID  string
	rootID     string
	ownerID    string
	sessionID  string
	ledger     *sourceledger.Store
	mutations  *projectsource.SourceMutationService
	projects   *project.SQLRegistry
	repos      *sessionstore.Repositories
	captures   *checkpointcontrol.Capture
	rewinds    *checkpointcontrol.Rewinds
	sourceRw   *sourcerewind.Service
}

func newTB06Env(t *testing.T, withGit bool) *tb06Env {
	t.Helper()
	ctx := t.Context()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "tb06.db")
	dataDir := filepath.Join(tmp, "data")
	contentDir := filepath.Join(dataDir, "source-content")
	projectDir := filepath.Join(tmp, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir projectDir: %v", err)
	}
	if withGit {
		tb06InitGitRepo(t, projectDir)
	}

	sqlDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := sqlDB.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})

	repos := sessionstore.NewSQL(sqlDB)
	owner, err := repos.People.HostOwner(ctx)
	if err != nil {
		t.Fatalf("host owner: %v", err)
	}

	projects := project.NewSQLRegistry(sqlDB)
	proj, err := projects.Create(ctx, project.CreateParams{
		Name:  "TB06 Fixture",
		Roots: []project.AttachRootParams{{Path: projectDir, Label: "primary"}},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if len(proj.Roots) == 0 {
		t.Fatalf("expected at least 1 project root")
	}

	ledger := sourceledger.New(sqlDB, contentDir, sourceledger.Timing{})
	mutations := projectsource.NewSourceMutationService(sqlDB, ledger)
	workspace := sessionscope.New(repos.Sessions)
	workspace.SetProjects(projects)
	captures := checkpointcontrol.NewCapture(dataDir, repos.Sessions, repos.Rewind, workspace)
	sourceRw := &sourcerewind.Service{Planner: ledger.Comparisons, Mutations: mutations}
	rewinds := checkpointcontrol.NewRewinds(
		repos.Sessions,
		repos.Transcript,
		repos.Rewind,
		captures,
		&promptstate.MutexRegistry{},
		workspace,
		projects,
		sourceRw,
		nil,
		checkpointcontrol.Runtime{
			WorkersInFlight:  func(context.Context, *api.Session) int { return 0 },
			ResetWorkers:     func(context.Context, *api.Session, string) {},
			ResetCoordinator: func(context.Context, string) {},
			ResetTurnLedgers: func(string, string) {},
			ResetProgress:    func(string) {},
			ResetQueue:       func(context.Context, string) {},
		},
	)

	sess, err := repos.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, proj.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := repos.Sessions.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspacePath = projectDir
	}); err != nil {
		t.Fatalf("bind workspace: %v", err)
	}

	return &tb06Env{
		dbPath:     dbPath,
		store:      sqlDB,
		contentDir: contentDir,
		dataDir:    dataDir,
		projectDir: projectDir,
		projectID:  proj.ID,
		rootID:     proj.Roots[0].ID,
		ownerID:    owner.ID,
		sessionID:  sess.ID,
		ledger:     ledger,
		mutations:  mutations,
		projects:   projects,
		repos:      repos,
		captures:   captures,
		rewinds:    rewinds,
		sourceRw:   sourceRw,
	}
}

func (e *tb06Env) callerCtx(t *testing.T) context.Context {
	t.Helper()
	owner, err := e.repos.People.HostOwner(t.Context())
	if err != nil {
		t.Fatalf("HostOwner: %v", err)
	}
	return people.WithCaller(t.Context(), owner)
}

func (e *tb06Env) project(t *testing.T) *project.Project {
	t.Helper()
	p, err := e.projects.Get(t.Context(), e.projectID)
	if err != nil {
		t.Fatalf("projects.Get: %v", err)
	}
	return p
}

func (e *tb06Env) appendUserAsk(t *testing.T, content string) (string, int) {
	t.Helper()
	ctx := t.Context()
	id := uuid.NewString()
	err := e.repos.Transcript.AppendMessages(ctx, e.sessionID, api.Message{
		ID:        id,
		Role:      api.MessageRoleUser,
		Origin:    api.MessageOriginUser,
		Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
		Content:   content,
	})
	if err != nil {
		t.Fatalf("append user ask: %v", err)
	}
	turnSource, err := e.repos.Transcript.ReadSourceTurn(ctx, e.sessionID, 0)
	if err != nil {
		t.Fatalf("ReadSourceTurn: %v", err)
	}
	return id, turnSource.Number
}

func (e *tb06Env) logReceipt(t *testing.T, family string, payload map[string]any) {
	t.Helper()
	payload["schema"] = "wea-tb06-receipt-v1"
	payload["family"] = family
	payload["lane"] = "in-process"
	payload["product_git_head"] = gitRev(t, "HEAD")
	payload["product_git_tree"] = gitRev(t, "HEAD^{tree}")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	t.Logf("WEA_TB06_RECEIPT:%s", raw)
}

// TestWEATB06E04AttributionAndBranchIdentity exercises E04:
// - Versions from every author kind: person, agent, worker, landed worker result,
//   command window, and external program.
// - Trunk, registered worktree branch, and worker branch identity.
// - Worker branch extends trunk file identity for an untouched path without moving trunk head.
// - Landing preserves authoring worker identity (job_id + origin).
// - Root-set mutation (attach/detach secondary root) preserves file identity and version history.
// - Independent read-only SQLite verification of source_operations, source_versions,
//   source_effects, and source_line_attr.
func TestWEATB06E04AttributionAndBranchIdentity(t *testing.T) {
	env := newTB06Env(t, true)
	ctx := env.callerCtx(t)

	// Create a real second Git worktree and register it in source_worktrees.
	wtDir := filepath.Join(t.TempDir(), "wt-feature")
	tb06RunGit(t, env.projectDir, "worktree", "add", "-q", "-b", "tb06-wt-branch", wtDir)
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", env.projectDir, "worktree", "remove", "--force", wtDir).Run()
	})

	wtRootChange, err := env.projects.AttachRoot(ctx, env.projectID, project.AttachRootParams{
		Path:  wtDir,
		Label: "worktree-feature",
	})
	if err != nil {
		t.Fatalf("AttachRoot worktree: %v", err)
	}
	wtID := "wt-" + uuid.NewString()[:8]
	wtBranch := sourcebranch.ForWorktree(wtID)
	nowStr := timelayout.Format(time.Now().UTC())
	if _, err := env.store.ExecContext(ctx, `
		INSERT INTO source_worktrees (id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at)
		VALUES (?, ?, 'repo-tb06', ?, ?, 'tb06-wt-branch', 'main', ?)
	`, wtID, env.projectID, env.projectDir, wtDir, nowStr); err != nil {
		t.Fatalf("insert source_worktrees: %v", err)
	}

	// 1. Person (user) creates multi-line file on trunk.
	v1 := []byte("line1-person\n")
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "collab.txt"},
		ProjectID:      env.projectID,
		BranchID:       sourcebranch.Trunk,
		Op:             api.SourceChangeOpCreate,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       env.ownerID,
		After:          v1,
	}); err != nil {
		t.Fatalf("record person create: %v", err)
	}
	trunkHead1, err := env.ledger.History.ResolveHead(ctx, env.projectID, sourcebranch.Trunk, env.rootID, "collab.txt")
	if err != nil {
		t.Fatalf("ResolveHead trunk v1: %v", err)
	}

	// 2. Agent appends line 2 on trunk.
	_, turn1 := env.appendUserAsk(t, "agent edit")
	v2 := []byte("line1-person\nline2-agent\n")
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "collab.txt"},
		ProjectID:      env.projectID,
		BranchID:       sourcebranch.Trunk,
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		SessionID:      env.sessionID,
		Turn:           turn1,
		ToolName:       "edit_file",
		Before:         v1,
		After:          v2,
	}); err != nil {
		t.Fatalf("record agent edit: %v", err)
	}
	trunkHead2, err := env.ledger.History.ResolveHead(ctx, env.projectID, sourcebranch.Trunk, env.rootID, "collab.txt")
	if err != nil {
		t.Fatalf("ResolveHead trunk v2: %v", err)
	}

	// 3. Worker writes on its own worker branch — must extend trunk FileID without moving trunk head.
	workerJobID := "job-tb06-worker-1"
	workerBranch, err := sourcebranch.ForWorker(workerJobID)
	if err != nil {
		t.Fatalf("ForWorker: %v", err)
	}
	vWorker := []byte("line1-person\nline2-agent\nline3-worker\n")
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "collab.txt"},
		ProjectID:      env.projectID,
		BranchID:       workerBranch,
		JobID:          workerJobID,
		SessionID:      env.sessionID,
		Turn:           turn1,
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		ActorLabel:     "worker-alpha",
		Before:         v2,
		After:          vWorker,
	}); err != nil {
		t.Fatalf("record worker branch edit: %v", err)
	}

	workerHead, err := env.ledger.History.ResolveHead(ctx, env.projectID, workerBranch, env.rootID, "collab.txt")
	if err != nil {
		t.Fatalf("ResolveHead worker: %v", err)
	}
	if workerHead.FileID != trunkHead1.FileID {
		t.Fatalf("worker branch forked FileID: got %s want %s", workerHead.FileID, trunkHead1.FileID)
	}
	trunkAfterWorker, err := env.ledger.History.ResolveHead(ctx, env.projectID, sourcebranch.Trunk, env.rootID, "collab.txt")
	if err != nil {
		t.Fatalf("ResolveHead trunk after worker: %v", err)
	}
	if trunkAfterWorker.VersionID != trunkHead2.VersionID {
		t.Fatalf("worker branch edit moved trunk head: got %s want %s", trunkAfterWorker.VersionID, trunkHead2.VersionID)
	}

	// 4. Landed worker result onto trunk — must preserve authoring worker's JobID & Origin.
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "collab.txt"},
		ProjectID:      env.projectID,
		BranchID:       sourcebranch.Trunk,
		JobID:          workerJobID,
		SessionID:      env.sessionID,
		Turn:           turn1,
		Cause:          sourceledger.CauseOverlayPromote,
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		ActorLabel:     "worker-alpha",
		Before:         v2,
		After:          vWorker,
	}); err != nil {
		t.Fatalf("record landed worker result: %v", err)
	}

	// Verify line-level attribution on trunk across person, agent, and landed worker.
	attrBeforeExt, err := env.ledger.History.QueryAttribution(ctx, env.projectID, sourcebranch.Trunk, env.rootID, "collab.txt")
	if err != nil {
		t.Fatalf("QueryAttribution trunk before external: %v", err)
	}
	if attrBeforeExt.HeadSHA256 != tb06SHA256Bytes(vWorker) {
		t.Fatalf("attribution head sha = %s, want %s", attrBeforeExt.HeadSHA256, tb06SHA256Bytes(vWorker))
	}
	if len(attrBeforeExt.Intervals) != 3 {
		t.Fatalf("expected 3 attributed line spans (person/agent/landed-worker), got %d: %+v", len(attrBeforeExt.Intervals), attrBeforeExt.Intervals)
	}
	wantOrigins := []api.SourceChangeOrigin{
		api.SourceChangeOriginUser,
		api.SourceChangeOriginAgent,
		api.SourceChangeOriginAgent, // landed worker
	}
	for i, wantOrigin := range wantOrigins {
		iv := attrBeforeExt.Intervals[i]
		if iv.StartLine != i+1 || iv.EndLine != i+1 {
			t.Fatalf("interval[%d] lines = [%d,%d], want [%d,%d]", i, iv.StartLine, iv.EndLine, i+1, i+1)
		}
		if iv.Origin != wantOrigin {
			t.Fatalf("interval[%d] origin = %q, want %q", i, iv.Origin, wantOrigin)
		}
	}
	if attrBeforeExt.Intervals[2].JobID != workerJobID {
		t.Fatalf("landed worker line 3 lost authoring JobID: got %q want %q", attrBeforeExt.Intervals[2].JobID, workerJobID)
	}

	// 5. Command window edit on trunk.
	cmdWinID := "cmdwin-" + uuid.NewString()[:8]
	if _, err := env.store.ExecContext(ctx, `
		INSERT INTO source_command_windows (id, project_id, session_id, turn, tool_call_id, tool_name, command_line, state, ordinal, started_ts)
		VALUES (?, ?, ?, ?, 'call-cmd-1', 'run_command', 'echo line4 >> collab.txt', 'ended', 1, ?)
	`, cmdWinID, env.projectID, env.sessionID, turn1, nowStr); err != nil {
		t.Fatalf("seed source_command_windows: %v", err)
	}
	vCmd := []byte("line1-person\nline2-agent\nline3-worker\nline4-command\n")
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation:  sourceledger.RecordLocation{RootID: env.rootID, Path: "collab.txt"},
		ProjectID:       env.projectID,
		BranchID:        sourcebranch.Trunk,
		SessionID:       env.sessionID,
		Turn:            turn1,
		CommandWindowID: cmdWinID,
		Cause:           sourceledger.CauseCommandWindow,
		Op:              api.SourceChangeOpWrite,
		Origin:          api.SourceChangeOriginExternal,
		Before:          vWorker,
		After:           vCmd,
	}); err != nil {
		t.Fatalf("record command edit: %v", err)
	}

	// 6. External program edit on trunk.
	vExt := []byte("line1-person\nline2-agent\nline3-worker\nline4-command\nline5-external\n")
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "collab.txt"},
		ProjectID:      env.projectID,
		BranchID:       sourcebranch.Trunk,
		Cause:          sourceledger.CauseFilesystemReconcile,
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginExternal,
		Before:         vCmd,
		After:          vExt,
	}); err != nil {
		t.Fatalf("record external edit: %v", err)
	}

	// 7. Registered worktree branch edit.
	vWT := []byte("worktree-line1\n")
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: wtRootChange.Added.ID, Path: "wt_only.txt"},
		ProjectID:      env.projectID,
		BranchID:       wtBranch,
		Op:             api.SourceChangeOpCreate,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       env.ownerID,
		After:          vWT,
	}); err != nil {
		t.Fatalf("record worktree create: %v", err)
	}

	// After external writes, head SHA advances and line attribution is cleared by design.
	attr, err := env.ledger.History.QueryAttribution(ctx, env.projectID, sourcebranch.Trunk, env.rootID, "collab.txt")
	if err != nil {
		t.Fatalf("QueryAttribution trunk after external: %v", err)
	}
	if attr.HeadSHA256 != tb06SHA256Bytes(vExt) {
		t.Fatalf("attribution head sha = %s, want %s", attr.HeadSHA256, tb06SHA256Bytes(vExt))
	}
	if len(attr.Intervals) != 0 {
		t.Fatalf("expected external write to clear line attribution, got %+v", attr.Intervals)
	}

	// Independent read-only SQLite verification of source_operations, source_versions, source_line_attr.
	ro, err := db.OpenReadOnly(ctx, env.dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	var wtBranchRecorded string
	if err := ro.QueryRowContext(ctx, `
		SELECT o.branch_id
		FROM source_operations o
		JOIN source_versions v ON v.operation_id = o.id
		WHERE v.project_id = ? AND v.root_id = ? AND v.path = 'wt_only.txt'
		ORDER BY v.seq DESC LIMIT 1
	`, env.projectID, wtRootChange.Added.ID).Scan(&wtBranchRecorded); err != nil {
		t.Fatalf("query worktree branch_id RO: %v", err)
	}
	if wtBranchRecorded != wtBranch.String() {
		t.Fatalf("worktree branch_id = %q, want %q", wtBranchRecorded, wtBranch.String())
	}

	var cmdOpOrigin, cmdOpWin string
	if err := ro.QueryRowContext(ctx, `
		SELECT origin, COALESCE(command_window_id, '') FROM source_operations
		WHERE project_id = ? AND cause = ?
	`, env.projectID, sourceledger.CauseCommandWindow).Scan(&cmdOpOrigin, &cmdOpWin); err != nil {
		t.Fatalf("query command op RO: %v", err)
	}
	if cmdOpOrigin != string(api.SourceChangeOriginExternal) || cmdOpWin != cmdWinID {
		t.Fatalf("command op mismatch: origin=%q win=%q", cmdOpOrigin, cmdOpWin)
	}

	var extOpCount int
	if err := ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM source_operations
		WHERE project_id = ? AND origin = 'external' AND cause = ?
	`, env.projectID, sourceledger.CauseFilesystemReconcile).Scan(&extOpCount); err != nil {
		t.Fatalf("query external op RO: %v", err)
	}
	if extOpCount != 1 {
		t.Fatalf("external reconcile op count = %d, want 1", extOpCount)
	}

	var workerBranchCount int
	if err := ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM source_operations WHERE project_id = ? AND branch_id = ? AND job_id = ?
	`, env.projectID, workerBranch.String(), workerJobID).Scan(&workerBranchCount); err != nil {
		t.Fatalf("query worker branch ops RO: %v", err)
	}
	if workerBranchCount != 1 {
		t.Fatalf("worker branch op count = %d, want 1", workerBranchCount)
	}

	var landedJobID, landedCause string
	if err := ro.QueryRowContext(ctx, `
		SELECT job_id, cause FROM source_operations
		WHERE project_id = ? AND branch_id = '' AND cause = ?
	`, env.projectID, sourceledger.CauseOverlayPromote).Scan(&landedJobID, &landedCause); err != nil {
		t.Fatalf("query landed op RO: %v", err)
	}
	if landedJobID != workerJobID {
		t.Fatalf("landed op job_id = %q, want %q", landedJobID, workerJobID)
	}

	// Root-set change (attach + detach an unrelated root) must preserve file identity and version history.
	beforeVersions, err := env.ledger.History.QueryFileVersions(ctx, env.projectID, trunkHead1.FileID, 50, 0)
	if err != nil {
		t.Fatalf("QueryFileVersions before root change: %v", err)
	}
	extraDir := t.TempDir()
	extraRoot, err := env.projects.AttachRoot(ctx, env.projectID, project.AttachRootParams{Path: extraDir, Label: "extra"})
	if err != nil {
		t.Fatalf("AttachRoot extra: %v", err)
	}
	if _, err := env.projects.DetachRoot(ctx, env.projectID, extraRoot.Added.ID); err != nil {
		t.Fatalf("DetachRoot extra: %v", err)
	}
	afterHead, err := env.ledger.History.ResolveHead(ctx, env.projectID, sourcebranch.Trunk, env.rootID, "collab.txt")
	if err != nil {
		t.Fatalf("ResolveHead after root change: %v", err)
	}
	if afterHead.FileID != trunkHead1.FileID {
		t.Fatalf("file identity changed after root-set mutation: %s vs %s", afterHead.FileID, trunkHead1.FileID)
	}
	afterVersions, err := env.ledger.History.QueryFileVersions(ctx, env.projectID, trunkHead1.FileID, 50, 0)
	if err != nil {
		t.Fatalf("QueryFileVersions after root change: %v", err)
	}
	if len(afterVersions.Versions) != len(beforeVersions.Versions) {
		t.Fatalf("version count changed across root-set mutation: %d vs %d", len(afterVersions.Versions), len(beforeVersions.Versions))
	}

	env.logReceipt(t, "E04", map[string]any{
		"trunk_file_id":        trunkHead1.FileID,
		"worker_branch":        workerBranch.String(),
		"worktree_branch":      wtBranch.String(),
		"attributed_spans":     len(attrBeforeExt.Intervals),
		"landed_worker_job_id": landedJobID,
		"versions_preserved":   len(afterVersions.Versions),
		"head_sha256":          attr.HeadSHA256,
	})
}

// TestWEATB06E05VersionsAndRestore exercises E05:
// - Restore retained versions of text, binary, and UTF-16LE (BOM) files.
// - Restore across rename, deletion, and recreated path with missing parent directories.
// - Restore a Git-produced version (`CauseGitOperation`).
// - Compare restored disk bytes with retained version bytes by SHA-256 hash.
// - Race a disk change (stale base SHA-256) and verify conflict refusal changes nothing on disk.
// - Make retained bytes unavailable and verify refusal changes nothing on disk.
func TestWEATB06E05VersionsAndRestore(t *testing.T) {
	env := newTB06Env(t, true)
	ctx := env.callerCtx(t)
	proj := env.project(t)

	// Helper to write file on disk and record in ledger.
	recordOnDisk := func(rel string, before, after []byte, op api.SourceChangeOp, cause string) sourceledger.BranchHead {
		t.Helper()
		abs := filepath.Join(env.projectDir, filepath.FromSlash(rel))
		if op == api.SourceChangeOpDelete {
			_ = os.Remove(abs)
		} else {
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", abs, err)
			}
			if err := os.WriteFile(abs, after, 0o644); err != nil {
				t.Fatalf("write %s: %v", abs, err)
			}
		}
		if err := env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: rel},
			ProjectID:      env.projectID,
			Op:             op,
			Origin:         api.SourceChangeOriginUser,
			PersonID:       env.ownerID,
			Cause:          cause,
			Before:         before,
			After:          after,
		}); err != nil {
			t.Fatalf("ledger.Record(%s, %s): %v", rel, op, err)
		}
		if op == api.SourceChangeOpDelete {
			return sourceledger.BranchHead{}
		}
		head, err := env.ledger.History.ResolveHead(ctx, env.projectID, sourcebranch.Trunk, env.rootID, rel)
		if err != nil {
			t.Fatalf("ResolveHead(%s): %v", rel, err)
		}
		return head
	}

	// 1. Text file restore.
	textV1 := []byte("alpha\nbeta\n")
	textV2 := []byte("alpha\nbeta-modified\n")
	textH1 := recordOnDisk("notes.txt", nil, textV1, api.SourceChangeOpCreate, "")
	recordOnDisk("notes.txt", textV1, textV2, api.SourceChangeOpWrite, "")
	verTextV1, err := env.ledger.History.ReadRestorableVersion(ctx, env.projectID, textH1.VersionID)
	if err != nil {
		t.Fatalf("ReadRestorableVersion text: %v", err)
	}
	resText, err := env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: verTextV1,
		FileID:  textH1.FileID,
		RootID:  env.rootID,
		Path:    "notes.txt",
		Base:    api.SourceTip{State: api.SourceTipStateContent, Sha256: tb06SHA256Bytes(textV2)},
	})
	if err != nil {
		t.Fatalf("Restore text: %v", err)
	}
	gotSHA, ok := tb06SHA256File(t, filepath.Join(env.projectDir, "notes.txt"))
	if !ok || gotSHA != tb06SHA256Bytes(textV1) || resText.SHA256 != tb06SHA256Bytes(textV1) {
		t.Fatalf("text restore sha mismatch: disk=%s res=%s want=%s", gotSHA, resText.SHA256, tb06SHA256Bytes(textV1))
	}

	// 2. Binary file restore (contains NUL bytes & non-UTF-8 sequences).
	binV1 := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0xFF, 0xFE, 0x42}
	binV2 := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x01, 0x02, 0x03}
	binH1 := recordOnDisk("assets/icon.bin", nil, binV1, api.SourceChangeOpCreate, "")
	recordOnDisk("assets/icon.bin", binV1, binV2, api.SourceChangeOpWrite, "")
	verBinV1, err := env.ledger.History.ReadRestorableVersion(ctx, env.projectID, binH1.VersionID)
	if err != nil {
		t.Fatalf("ReadRestorableVersion binary: %v", err)
	}
	if _, err := env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: verBinV1,
		FileID:  binH1.FileID,
		RootID:  env.rootID,
		Path:    "assets/icon.bin",
		Base:    api.SourceTip{State: api.SourceTipStateContent, Sha256: tb06SHA256Bytes(binV2)},
	}); err != nil {
		t.Fatalf("Restore binary: %v", err)
	}
	gotBinSHA, ok := tb06SHA256File(t, filepath.Join(env.projectDir, "assets", "icon.bin"))
	if !ok || gotBinSHA != tb06SHA256Bytes(binV1) {
		t.Fatalf("binary restore sha mismatch: got=%s want=%s", gotBinSHA, tb06SHA256Bytes(binV1))
	}

	// 3. UTF-16LE with BOM file restore.
	utf16V1 := []byte{0xFF, 0xFE, 'H', 0x00, 'i', 0x00, '\n', 0x00}
	utf16V2 := []byte{0xFF, 0xFE, 'B', 0x00, 'y', 0x00, 'e', 0x00, '\n', 0x00}
	utf16H1 := recordOnDisk("i18n/utf16.txt", nil, utf16V1, api.SourceChangeOpCreate, "")
	recordOnDisk("i18n/utf16.txt", utf16V1, utf16V2, api.SourceChangeOpWrite, "")
	verUTF16V1, err := env.ledger.History.ReadRestorableVersion(ctx, env.projectID, utf16H1.VersionID)
	if err != nil {
		t.Fatalf("ReadRestorableVersion utf16: %v", err)
	}
	if _, err := env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: verUTF16V1,
		FileID:  utf16H1.FileID,
		RootID:  env.rootID,
		Path:    "i18n/utf16.txt",
		Base:    api.SourceTip{State: api.SourceTipStateContent, Sha256: tb06SHA256Bytes(utf16V2)},
	}); err != nil {
		t.Fatalf("Restore utf16: %v", err)
	}
	gotUTF16SHA, ok := tb06SHA256File(t, filepath.Join(env.projectDir, "i18n", "utf16.txt"))
	if !ok || gotUTF16SHA != tb06SHA256Bytes(utf16V1) {
		t.Fatalf("utf16 restore sha mismatch: got=%s want=%s", gotUTF16SHA, tb06SHA256Bytes(utf16V1))
	}

	// 4. Restore across rename (old.txt -> moved/new.txt, restore v1 content onto current path).
	renV1 := []byte("original before rename\n")
	renV2 := []byte("edited after rename\n")
	renH1 := recordOnDisk("old.txt", nil, renV1, api.SourceChangeOpCreate, "")
	if err := os.MkdirAll(filepath.Join(env.projectDir, "moved"), 0o755); err != nil {
		t.Fatalf("mkdir moved: %v", err)
	}
	if err := os.Rename(filepath.Join(env.projectDir, "old.txt"), filepath.Join(env.projectDir, "moved", "new.txt")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, FromRootID: env.rootID, FromPath: "old.txt", Path: "moved/new.txt"},
		ProjectID:      env.projectID,
		Op:             api.SourceChangeOpRename,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       env.ownerID,
		Before:         renV1,
		After:          renV1,
	}); err != nil {
		t.Fatalf("record rename: %v", err)
	}
	recordOnDisk("moved/new.txt", renV1, renV2, api.SourceChangeOpWrite, "")
	verRenV1, err := env.ledger.History.ReadRestorableVersion(ctx, env.projectID, renH1.VersionID)
	if err != nil {
		t.Fatalf("ReadRestorableVersion rename: %v", err)
	}
	if _, err := env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: verRenV1,
		FileID:  renH1.FileID,
		RootID:  env.rootID,
		Path:    "moved/new.txt",
		Base:    api.SourceTip{State: api.SourceTipStateContent, Sha256: tb06SHA256Bytes(renV2)},
	}); err != nil {
		t.Fatalf("Restore across rename: %v", err)
	}
	gotRenSHA, ok := tb06SHA256File(t, filepath.Join(env.projectDir, "moved", "new.txt"))
	if !ok || gotRenSHA != tb06SHA256Bytes(renV1) {
		t.Fatalf("rename restore sha mismatch: got=%s want=%s", gotRenSHA, tb06SHA256Bytes(renV1))
	}

	// 5. Restore across deletion and missing parent directories.
	delV1 := []byte("deleted deep content\n")
	delH1 := recordOnDisk("deep/nested/gone.txt", nil, delV1, api.SourceChangeOpCreate, "")
	recordOnDisk("deep/nested/gone.txt", delV1, nil, api.SourceChangeOpDelete, "")
	if err := os.RemoveAll(filepath.Join(env.projectDir, "deep")); err != nil {
		t.Fatalf("remove parent dirs: %v", err)
	}
	verDelV1, err := env.ledger.History.ReadRestorableVersion(ctx, env.projectID, delH1.VersionID)
	if err != nil {
		t.Fatalf("ReadRestorableVersion deleted: %v", err)
	}
	if _, err := env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: verDelV1,
		FileID:  delH1.FileID,
		RootID:  env.rootID,
		Path:    "deep/nested/gone.txt",
		Base:    api.SourceTip{State: api.SourceTipStateAbsent},
	}); err != nil {
		t.Fatalf("Restore deleted with missing parents: %v", err)
	}
	gotDelSHA, ok := tb06SHA256File(t, filepath.Join(env.projectDir, "deep", "nested", "gone.txt"))
	if !ok || gotDelSHA != tb06SHA256Bytes(delV1) {
		t.Fatalf("deleted restore sha mismatch: got=%s want=%s", gotDelSHA, tb06SHA256Bytes(delV1))
	}

	// 6. Git-produced version restore.
	gitV1 := []byte("from git checkout\n")
	gitV2 := []byte("dirty working copy\n")
	gitH1 := recordOnDisk("tracked.txt", nil, gitV1, api.SourceChangeOpCreate, sourceledger.CauseGitOperation)
	recordOnDisk("tracked.txt", gitV1, gitV2, api.SourceChangeOpWrite, "")
	verGitV1, err := env.ledger.History.ReadRestorableVersion(ctx, env.projectID, gitH1.VersionID)
	if err != nil {
		t.Fatalf("ReadRestorableVersion git: %v", err)
	}
	if _, err := env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: verGitV1,
		FileID:  gitH1.FileID,
		RootID:  env.rootID,
		Path:    "tracked.txt",
		Base:    api.SourceTip{State: api.SourceTipStateContent, Sha256: tb06SHA256Bytes(gitV2)},
	}); err != nil {
		t.Fatalf("Restore git version: %v", err)
	}
	gotGitSHA, ok := tb06SHA256File(t, filepath.Join(env.projectDir, "tracked.txt"))
	if !ok || gotGitSHA != tb06SHA256Bytes(gitV1) {
		t.Fatalf("git restore sha mismatch: got=%s want=%s", gotGitSHA, tb06SHA256Bytes(gitV1))
	}

	// 7. Race a disk change -> conflict refusal MUST leave disk untouched.
	raceBytes := []byte("concurrent disk edit\n")
	notesPath := filepath.Join(env.projectDir, "notes.txt")
	if err := os.WriteFile(notesPath, raceBytes, 0o644); err != nil {
		t.Fatalf("simulate concurrent disk edit: %v", err)
	}
	preRaceTree := tb06HashTree(t, env.projectDir)
	_, err = env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: verTextV1,
		FileID:  textH1.FileID,
		RootID:  env.rootID,
		Path:    "notes.txt",
		Base:    api.SourceTip{State: api.SourceTipStateContent, Sha256: tb06SHA256Bytes(textV1)}, // stale!
	})
	if err == nil {
		t.Fatalf("expected conflict error on stale disk base, got nil")
	}
	postRaceTree := tb06HashTree(t, env.projectDir)
	if !tb06EqualHashMaps(preRaceTree, postRaceTree) {
		t.Fatalf("refused disk-conflict restore mutated working tree")
	}

	// 8. Unavailable bytes refusal -> MUST leave disk untouched.
	preUnavailTree := tb06HashTree(t, env.projectDir)
	badVersion := verTextV1
	badVersion.Content = []byte("corrupted bytes not matching sha")
	_, err = env.mutations.Versions.Restore(ctx, uuid.NewString(), proj, projectsource.SourceVersionRestoreRequest{
		Version: badVersion,
		FileID:  textH1.FileID,
		RootID:  env.rootID,
		Path:    "notes.txt",
		Base:    api.SourceTip{State: api.SourceTipStateContent, Sha256: tb06SHA256Bytes(raceBytes)},
	})
	if !errors.Is(err, sourceledger.ErrVersionUnavailable) {
		t.Fatalf("expected ErrVersionUnavailable, got %v", err)
	}
	postUnavailTree := tb06HashTree(t, env.projectDir)
	if !tb06EqualHashMaps(preUnavailTree, postUnavailTree) {
		t.Fatalf("refused unavailable-bytes restore mutated working tree")
	}

	env.logReceipt(t, "E05", map[string]any{
		"text_sha256":             gotSHA,
		"binary_sha256":           gotBinSHA,
		"utf16_sha256":            gotUTF16SHA,
		"rename_sha256":           gotRenSHA,
		"deleted_recreate_sha256": gotDelSHA,
		"git_version_sha256":      gotGitSHA,
		"disk_conflict_refused":   true,
		"unavailable_refused":     true,
	})
}

// TestWEATB06E06RewindBlockersAndBoundaries exercises E06:
// - Reaches rewind blockers (working_file_changed, later_change, intervening_change,
//   command_authorship_unavailable, unsaved_agent_change, version_unavailable,
//   workspace_unavailable, destination_occupied, unrecorded_change, stale plan_digest,
//   and ineligible anchor) and verifies each leaves working tree (SHA-256) and
//   SQLite transcript/ledger row counts untouched.
// - Clean rewind + idempotent retry with same operation_id (no duplicate versions).
// - Failed rewind midway -> compensation restores prior files without partial suffix.
// - Restarted rewind mid-apply -> resumes cleanly with no duplicate source_versions.
func TestWEATB06E06RewindBlockersAndBoundaries(t *testing.T) {
	trackedTables := []string{"messages", "source_operations", "source_versions", "source_effects"}

	// Helper to assert a rewind blocker leaves disk AND SQLite untouched.
	assertBlockedUntouched := func(t *testing.T, env *tb06Env, wantCode string, trigger func(context.Context) error) {
		t.Helper()
		ctx := env.callerCtx(t)
		preTree := tb06HashTree(t, env.projectDir)
		preCounts := tb06TableCountsRO(t, ctx, env.dbPath, trackedTables)

		err := trigger(ctx)
		if err == nil {
			t.Fatalf("blocker %s: expected error, got nil", wantCode)
		}
		var blocked *sourceledger.RewindBlockedError
		if errors.As(err, &blocked) {
			found := false
			for _, iss := range blocked.Issues {
				if iss.Code == wantCode {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("blocker %s: got issues %+v", wantCode, blocked.Issues)
			}
		} else if wantCode != "plan_changed" && wantCode != "anchor_ineligible" {
			t.Fatalf("blocker %s: expected RewindBlockedError, got %v", wantCode, err)
		}

		postTree := tb06HashTree(t, env.projectDir)
		postCounts := tb06TableCountsRO(t, ctx, env.dbPath, trackedTables)
		if !tb06EqualHashMaps(preTree, postTree) {
			t.Fatalf("blocker %s mutated working tree on disk", wantCode)
		}
		if !tb06EqualCountMaps(preCounts, postCounts) {
			t.Fatalf("blocker %s mutated SQLite row counts: pre=%v post=%v", wantCode, preCounts, postCounts)
		}
	}

	reachedBlockers := []string{}

	t.Run("blocker_working_file_changed", func(t *testing.T) {
		env := newTB06Env(t, false)
		anchor, turn := env.appendUserAsk(t, "edit a.txt")
		p := filepath.Join(env.projectDir, "a.txt")
		_ = os.WriteFile(p, []byte("agent-v1"), 0o644)
		if err := env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "a.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("agent-v1"),
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
		preview, err := env.rewinds.PreviewRewind(env.callerCtx(t), env.sessionID, anchor)
		if err != nil || preview.PlanDigest == "" {
			t.Fatalf("preview: %+v err=%v", preview, err)
		}
		_ = os.WriteFile(p, []byte("unobserved-human-edit"), 0o644)
		assertBlockedUntouched(t, env, "working_file_changed", func(ctx context.Context) error {
			_, err := env.rewinds.RewindToPrompt(ctx, uuid.NewString(), env.sessionID, anchor, preview.PlanDigest)
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "working_file_changed")

	t.Run("blocker_later_change", func(t *testing.T) {
		env := newTB06Env(t, false)
		anchor, turn := env.appendUserAsk(t, "edit b.txt")
		p := filepath.Join(env.projectDir, "b.txt")
		_ = os.WriteFile(p, []byte("later-human"), 0o644)
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "b.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("agent"),
		})
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "b.txt"},
			ProjectID:      env.projectID,
			Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
			Before:         []byte("agent"), After: []byte("later-human"),
		})
		assertBlockedUntouched(t, env, "later_change", func(ctx context.Context) error {
			_, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{anchor})
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "later_change")

	t.Run("blocker_intervening_change", func(t *testing.T) {
		env := newTB06Env(t, false)
		a1, t1 := env.appendUserAsk(t, "turn 1")
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "c.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: t1,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("t1"),
		})
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "c.txt"},
			ProjectID:      env.projectID,
			Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
			Before:         []byte("t1"), After: []byte("ext-between"),
		})
		a2, t2 := env.appendUserAsk(t, "turn 2")
		_ = os.WriteFile(filepath.Join(env.projectDir, "c.txt"), []byte("t2"), 0o644)
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "c.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: t2,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
			Before: []byte("ext-between"), After: []byte("t2"),
		})
		assertBlockedUntouched(t, env, "intervening_change", func(ctx context.Context) error {
			_, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{a1, a2})
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "intervening_change")

	t.Run("blocker_command_authorship_unavailable", func(t *testing.T) {
		env := newTB06Env(t, false)
		anchor, turn := env.appendUserAsk(t, "run shell")
		cmdWinID := "cw-" + uuid.NewString()[:8]
		nowStr := timelayout.Format(time.Now().UTC())
		if _, err := env.store.ExecContext(t.Context(), `
			INSERT INTO source_command_windows (id, project_id, session_id, turn, tool_call_id, tool_name, command_line, state, ordinal, started_ts)
			VALUES (?, ?, ?, ?, 'call-cw', 'run_command', 'echo hi > cmd.txt', 'ended', 1, ?)
		`, cmdWinID, env.projectID, env.sessionID, turn, nowStr); err != nil {
			t.Fatalf("seed cmd window: %v", err)
		}
		_ = os.WriteFile(filepath.Join(env.projectDir, "cmd.txt"), []byte("hi\n"), 0o644)
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation:  sourceledger.RecordLocation{RootID: env.rootID, Path: "cmd.txt"},
			ProjectID:       env.projectID, SessionID: env.sessionID, Turn: turn,
			CommandWindowID: cmdWinID, Cause: sourceledger.CauseCommandWindow,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginExternal, After: []byte("hi\n"),
		})
		assertBlockedUntouched(t, env, "command_authorship_unavailable", func(ctx context.Context) error {
			_, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{anchor})
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "command_authorship_unavailable")

	t.Run("blocker_unsaved_agent_change", func(t *testing.T) {
		env := newTB06Env(t, false)
		anchor, turn := env.appendUserAsk(t, "held edit")
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "held.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("held"),
		})
		if _, err := env.store.ExecContext(t.Context(),
			`UPDATE source_versions SET landing = ? WHERE project_id = ? AND path = 'held.txt'`,
			sourceledger.LandingEditorDocument, env.projectID,
		); err != nil {
			t.Fatalf("mark landing editor_document: %v", err)
		}
		assertBlockedUntouched(t, env, "unsaved_agent_change", func(ctx context.Context) error {
			_, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{anchor})
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "unsaved_agent_change")

	t.Run("blocker_version_unavailable", func(t *testing.T) {
		env := newTB06Env(t, false)
		anchor, turn := env.appendUserAsk(t, "evicted blob")
		_ = os.WriteFile(filepath.Join(env.projectDir, "evicted.txt"), []byte("payload"), 0o644)
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "evicted.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("payload"),
		})
		// Remove all blob files from contentDir to simulate unavailable retained bytes.
		if err := os.RemoveAll(env.contentDir); err != nil {
			t.Fatalf("remove contentDir: %v", err)
		}
		assertBlockedUntouched(t, env, "version_unavailable", func(ctx context.Context) error {
			_, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{anchor})
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "version_unavailable")

	t.Run("blocker_workspace_unavailable", func(t *testing.T) {
		env := newTB06Env(t, false)
		anchor, turn := env.appendUserAsk(t, "detached root")
		_ = os.WriteFile(filepath.Join(env.projectDir, "w.txt"), []byte("w"), 0o644)
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "w.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("w"),
		})
		projMissingRoot := env.project(t)
		projMissingRoot.Roots = nil
		assertBlockedUntouched(t, env, "workspace_unavailable", func(ctx context.Context) error {
			_, err := env.sourceRw.Prepare(ctx, projMissingRoot, env.sessionID, []string{anchor})
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "workspace_unavailable")

	t.Run("blocker_destination_occupied", func(t *testing.T) {
		env := newTB06Env(t, false)
		anchor, turn := env.appendUserAsk(t, "rename file")
		_ = os.WriteFile(filepath.Join(env.projectDir, "dst.txt"), []byte("body"), 0o644)
		_ = env.ledger.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, FromRootID: env.rootID, FromPath: "src.txt", Path: "dst.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpRename, Origin: api.SourceChangeOriginAgent,
			Before: []byte("body"), After: []byte("body"),
		})
		// Occupy original src.txt on disk before rewind.
		_ = os.WriteFile(filepath.Join(env.projectDir, "src.txt"), []byte("occupier"), 0o644)
		assertBlockedUntouched(t, env, "destination_occupied", func(ctx context.Context) error {
			_, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{anchor})
			return err
		})
	})
	reachedBlockers = append(reachedBlockers, "destination_occupied")

	t.Run("blocker_unrecorded_change_and_stale_plan_digest", func(t *testing.T) {
		env := newTB06Env(t, false)
		ctx := env.callerCtx(t)
		first, _ := env.appendUserAsk(t, "first ask")
		preview, err := env.rewinds.PreviewRewind(ctx, env.sessionID, first)
		if err != nil || preview.PlanDigest == "" {
			t.Fatalf("preview: %+v err=%v", preview, err)
		}
		second, _ := env.appendUserAsk(t, "second ask")

		// Stale plan_digest after transcript grew MUST fail with ErrRewindPlanChanged and leave DB untouched.
		assertBlockedUntouched(t, env, "plan_changed", func(ctx context.Context) error {
			_, err := env.rewinds.RewindToPrompt(ctx, uuid.NewString(), env.sessionID, first, preview.PlanDigest)
			if !errors.Is(err, checkpointcontrol.ErrRewindPlanChanged) {
				t.Fatalf("expected ErrRewindPlanChanged, got %v", err)
			}
			return err
		})

		// Unrecorded change in later turn MUST be reported by PreviewRewind with empty PlanDigest.
		cp := sessioncheckpoint.New(env.dataDir, env.projectDir, env.repos.Rewind)
		if _, err := cp.Open(ctx, env.sessionID, second); err != nil {
			t.Fatalf("cp.Open: %v", err)
		}
		if err := cp.CapturePreImage(ctx, env.sessionID, second, "untracked.md", env.rootID); err != nil {
			t.Fatalf("CapturePreImage: %v", err)
		}
		_ = os.WriteFile(filepath.Join(env.projectDir, "untracked.md"), []byte("host-only"), 0o644)
		unrecPreview, err := env.rewinds.PreviewRewind(ctx, env.sessionID, first)
		if err != nil {
			t.Fatalf("PreviewRewind unrecorded: %v", err)
		}
		if len(unrecPreview.Issues) != 1 || unrecPreview.Issues[0].Code != "unrecorded_change" || unrecPreview.PlanDigest != "" {
			t.Fatalf("expected unrecorded_change issue with empty digest, got %+v", unrecPreview)
		}
	})
	reachedBlockers = append(reachedBlockers, "unrecorded_change", "plan_changed")

	// Clean rewind + idempotent retry (no duplicate versions or partial suffix).
	t.Run("clean_rewind_and_idempotent_retry", func(t *testing.T) {
		env := newTB06Env(t, false)
		ctx := env.callerCtx(t)
		baseBytes := []byte("base-content\n")
		editedBytes := []byte("agent-edited\n")
		basePath := filepath.Join(env.projectDir, "existing.txt")
		_ = os.WriteFile(basePath, baseBytes, 0o644)
		if err := env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "existing.txt"},
			ProjectID:      env.projectID, Op: api.SourceChangeOpCreate,
			Origin: api.SourceChangeOriginUser, PersonID: env.ownerID, After: baseBytes,
		}); err != nil {
			t.Fatalf("seed existing: %v", err)
		}

		anchor, turn := env.appendUserAsk(t, "edit existing and create new")
		_ = os.WriteFile(basePath, editedBytes, 0o644)
		_ = env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "existing.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
			Before: baseBytes, After: editedBytes,
		})
		_ = os.WriteFile(filepath.Join(env.projectDir, "created.txt"), []byte("temp\n"), 0o644)
		_ = env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "created.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
			After: []byte("temp\n"),
		})

		preview, err := env.rewinds.PreviewRewind(ctx, env.sessionID, anchor)
		if err != nil || len(preview.Issues) != 0 || preview.PlanDigest == "" {
			t.Fatalf("clean preview failed: %+v err=%v", preview, err)
		}
		opID := uuid.NewString()
		res, err := env.rewinds.RewindToPrompt(ctx, opID, env.sessionID, anchor, preview.PlanDigest)
		if err != nil {
			t.Fatalf("RewindToPrompt: %v", err)
		}
		if res.TruncatedMessageCount != 1 || len(res.RestoredPaths) != 2 {
			t.Fatalf("unexpected rewind result: %+v", res)
		}
		gotBaseSHA, ok := tb06SHA256File(t, basePath)
		if !ok || gotBaseSHA != tb06SHA256Bytes(baseBytes) {
			t.Fatalf("existing.txt not restored to base sha: got=%s want=%s", gotBaseSHA, tb06SHA256Bytes(baseBytes))
		}
		if _, exists := tb06SHA256File(t, filepath.Join(env.projectDir, "created.txt")); exists {
			t.Fatalf("created.txt was not removed by rewind")
		}
		countsAfterFirst := tb06TableCountsRO(t, ctx, env.dbPath, trackedTables)

		// Idempotent replay with same operation_id MUST NOT create duplicate versions/effects.
		replay, err := env.rewinds.RewindToPrompt(ctx, opID, env.sessionID, anchor, preview.PlanDigest)
		if err != nil {
			t.Fatalf("replay RewindToPrompt: %v", err)
		}
		if replay.TruncatedMessageCount != res.TruncatedMessageCount {
			t.Fatalf("replay truncated=%d want %d", replay.TruncatedMessageCount, res.TruncatedMessageCount)
		}
		countsAfterReplay := tb06TableCountsRO(t, ctx, env.dbPath, trackedTables)
		if !tb06EqualCountMaps(countsAfterFirst, countsAfterReplay) {
			t.Fatalf("idempotent retry duplicated rows: first=%v replay=%v", countsAfterFirst, countsAfterReplay)
		}
	})

	// Failed rewind midway -> Rollback compensates completed file 0 without partial suffix.
	t.Run("failed_rewind_midway_compensates_without_partial_suffix", func(t *testing.T) {
		env := newTB06Env(t, false)
		ctx := env.callerCtx(t)
		anchor, turn := env.appendUserAsk(t, "create two files")
		f1 := filepath.Join(env.projectDir, "f1.txt")
		f2 := filepath.Join(env.projectDir, "f2.txt")
		_ = os.WriteFile(f1, []byte("one"), 0o644)
		_ = os.WriteFile(f2, []byte("two"), 0o644)
		_ = env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "f1.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("one"),
		})
		_ = env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "f2.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("two"),
		})

		op, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{anchor})
		if err != nil || len(op.Files) != 2 {
			t.Fatalf("Prepare 2 files: op=%+v err=%v", op, err)
		}
		op.PersonID = env.ownerID

		// Inject a persistence failure after file 0 finishes and before file 1 runs.
		saveCalls := 0
		applyErr := env.sourceRw.Apply(ctx, env.project(t), op, func() error {
			saveCalls++
			// saveCalls: 1=applying, 2=pending(f0), 3=applied(f0), 4=pending(f1) -> fail here!
			if saveCalls == 4 {
				return errors.New("simulated crash before file 1")
			}
			return nil
		})
		if applyErr == nil {
			t.Fatalf("expected apply error on simulated crash")
		}
		if op.Phase != "rolled_back" || op.Applied != 0 {
			t.Fatalf("expected rolled_back with Applied=0, got phase=%q applied=%d", op.Phase, op.Applied)
		}
		// Both f1.txt and f2.txt MUST be back to their pre-rewind bytes ("one" and "two").
		sha1, ok1 := tb06SHA256File(t, f1)
		sha2, ok2 := tb06SHA256File(t, f2)
		if !ok1 || sha1 != tb06SHA256Bytes([]byte("one")) {
			t.Fatalf("f1 not compensated after failed rewind: exists=%v sha=%s", ok1, sha1)
		}
		if !ok2 || sha2 != tb06SHA256Bytes([]byte("two")) {
			t.Fatalf("f2 altered during failed rewind: exists=%v sha=%s", ok2, sha2)
		}
	})

	// Restarted rewind mid-apply -> resumes from persisted Operation without duplicate versions.
	t.Run("restarted_rewind_mid_apply_no_duplicate_version", func(t *testing.T) {
		env := newTB06Env(t, false)
		ctx := env.callerCtx(t)
		anchor, turn := env.appendUserAsk(t, "create two files for restart")
		f1 := filepath.Join(env.projectDir, "r1.txt")
		f2 := filepath.Join(env.projectDir, "r2.txt")
		_ = os.WriteFile(f1, []byte("r1-data"), 0o644)
		_ = os.WriteFile(f2, []byte("r2-data"), 0o644)
		_ = env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "r1.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("r1-data"),
		})
		_ = env.ledger.Record(ctx, sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "r2.txt"},
			ProjectID:      env.projectID, SessionID: env.sessionID, Turn: turn,
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("r2-data"),
		})

		op, err := env.sourceRw.Prepare(ctx, env.project(t), env.sessionID, []string{anchor})
		if err != nil || len(op.Files) != 2 {
			t.Fatalf("Prepare restart test: op=%+v err=%v", op, err)
		}
		op.PersonID = env.ownerID

		preCounts := tb06TableCountsRO(t, ctx, env.dbPath, trackedTables)

		// Simulate a hard process crash right after file 0 finishes and before file 1 runs:
		// run file 0 under the same AttemptID, then persist the 2-file operation at Phase="applying", Applied=1.
		opFile0 := &sourcerewind.Operation{
			AttemptID:   op.AttemptID,
			ProjectID:   op.ProjectID,
			WorkspaceID: op.WorkspaceID,
			PersonID:    op.PersonID,
			Files:       op.Files[:1],
			Phase:       "prepared",
		}
		if err := env.sourceRw.Apply(ctx, env.project(t), opFile0, func() error { return nil }); err != nil {
			t.Fatalf("pre-crash file 0 Apply: %v", err)
		}
		op.Phase = "applying"
		op.Applied = 1
		op.Pending = false
		persisted, err := json.Marshal(op)
		if err != nil {
			t.Fatalf("marshal mid-apply op: %v", err)
		}

		// Resume after restart from the persisted mid-apply state (Applied=1, Pending=false).
		var resumed sourcerewind.Operation
		if err := json.Unmarshal(persisted, &resumed); err != nil {
			t.Fatalf("unmarshal resumed op: %v", err)
		}
		if err := env.sourceRw.Apply(ctx, env.project(t), &resumed, func() error { return nil }); err != nil {
			t.Fatalf("resumed Apply: %v", err)
		}
		if resumed.Phase != "files_applied" || resumed.Applied != 2 {
			t.Fatalf("resumed op state = phase:%q applied:%d", resumed.Phase, resumed.Applied)
		}
		countsAfterResume := tb06TableCountsRO(t, ctx, env.dbPath, trackedTables)
		// Exactly 2 new rewind versions/effects (1 for file 0 before crash + 1 for file 1 after resume), no duplicates!
		if countsAfterResume["source_versions"] != preCounts["source_versions"]+2 ||
			countsAfterResume["source_effects"] != preCounts["source_effects"]+2 {
			t.Fatalf("restarted rewind version/effect count mismatch: pre=%v post=%v", preCounts, countsAfterResume)
		}
		// Calling Apply again on a completed ("files_applied") operation is a no-op with zero duplicate rows.
		if err := env.sourceRw.Apply(ctx, env.project(t), &resumed, func() error { return nil }); err != nil {
			t.Fatalf("idempotent completed Apply: %v", err)
		}
		countsAfterIdempotent := tb06TableCountsRO(t, ctx, env.dbPath, trackedTables)
		if !tb06EqualCountMaps(countsAfterResume, countsAfterIdempotent) {
			t.Fatalf("idempotent Apply duplicated rows: resume=%v idemp=%v", countsAfterResume, countsAfterIdempotent)
		}
		if _, exists := tb06SHA256File(t, f1); exists {
			t.Fatalf("r1.txt still exists after resumed rewind")
		}
		if _, exists := tb06SHA256File(t, f2); exists {
			t.Fatalf("r2.txt still exists after resumed rewind")
		}
	})

	sort.Strings(reachedBlockers)
	env := newTB06Env(t, false)
	env.logReceipt(t, "E06", map[string]any{
		"blockers_reached":         reachedBlockers,
		"clean_rewind_verified":    true,
		"idempotent_retry_clean":   true,
		"midway_compensation_ok":   true,
		"restart_no_dup_versions":  true,
	})
}

// TestWEATB06E07FilesMarksAndCounts exercises E07 (headless API/DB projection):
// - Produces a known set of effects across multiple files (agent, external, user, worker branch).
// - Compares Files marks (`Walk.QueryWalk(BaselinePresentation)`) and unpresented AI counts
//   against independently counted rows in `db.OpenReadOnly`.
// - Opens each marked file via `Comparisons.CompareScope(BaselinePresentation)` and verifies
//   it resolves to the exact comparison (`InRange == true`, expected Before/After).
// - Verifies `CompletePresentation` clears marks and `WithdrawPresentation` restores them,
//   and re-resolving produces identical deterministic answers.
func TestWEATB06E07FilesMarksAndCounts(t *testing.T) {
	env := newTB06Env(t, false)
	ctx := env.callerCtx(t)

	// File 1: 2 agent effects on trunk.
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "ai_a.go"},
		ProjectID:      env.projectID, Op: api.SourceChangeOpCreate,
		Origin: api.SourceChangeOriginAgent, After: []byte("package a\n"),
	}); err != nil {
		t.Fatalf("record ai_a v1: %v", err)
	}
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "ai_a.go"},
		ProjectID:      env.projectID, Op: api.SourceChangeOpWrite,
		Origin: api.SourceChangeOriginAgent, Before: []byte("package a\n"), After: []byte("package a\n// v2\n"),
	}); err != nil {
		t.Fatalf("record ai_a v2: %v", err)
	}

	// File 2: 1 agent effect on trunk.
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "ai_b.go"},
		ProjectID:      env.projectID, Op: api.SourceChangeOpCreate,
		Origin: api.SourceChangeOriginAgent, After: []byte("package b\n"),
	}); err != nil {
		t.Fatalf("record ai_b: %v", err)
	}

	// File 3: 1 external effect on trunk (marked in walk, NOT an agent presentation).
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "ext_c.go"},
		ProjectID:      env.projectID, Op: api.SourceChangeOpCreate,
		Origin: api.SourceChangeOriginExternal, After: []byte("package c\n"),
	}); err != nil {
		t.Fatalf("record ext_c: %v", err)
	}

	// File 4: worker branch agent effect — MUST NOT appear in trunk Files marks or counts.
	wb, err := sourcebranch.ForWorker("job-e07-worker")
	if err != nil {
		t.Fatalf("ForWorker: %v", err)
	}
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "worker_only.go"},
		ProjectID:      env.projectID, BranchID: wb, JobID: "job-e07-worker",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("package w\n"),
	}); err != nil {
		t.Fatalf("record worker_only: %v", err)
	}

	// Query product Files marks via Walk.QueryWalk(BaselinePresentation).
	walk1, err := env.ledger.Walk.QueryWalk(ctx, env.projectID, sourceledger.Baseline{Kind: sourceledger.BaselinePresentation}, 100, 0, sourceledger.CommitLens{})
	if err != nil {
		t.Fatalf("QueryWalk #1: %v", err)
	}
	// Re-resolve and verify identical deterministic answer.
	walk2, err := env.ledger.Walk.QueryWalk(ctx, env.projectID, sourceledger.Baseline{Kind: sourceledger.BaselinePresentation}, 100, 0, sourceledger.CommitLens{})
	if err != nil {
		t.Fatalf("QueryWalk #2: %v", err)
	}
	if len(walk1.Files) != len(walk2.Files) {
		t.Fatalf("non-deterministic walk resolution: %d vs %d", len(walk1.Files), len(walk2.Files))
	}

	// Independent read-only SQLite counts of addressable trunk effects and unpresented agent effects.
	ro, err := db.OpenReadOnly(ctx, env.dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	var independentTrunkFiles, independentAgentFiles, independentAgentEffects int
	if err := ro.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT e.file_id)
		FROM source_effects e
		JOIN source_operations o ON o.id = e.operation_id
		WHERE e.project_id = ? AND e.walk_visible = 1 AND o.branch_id = ''
	`, env.projectID).Scan(&independentTrunkFiles); err != nil {
		t.Fatalf("independent trunk file count: %v", err)
	}
	if err := ro.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT file_id), COUNT(*)
		FROM source_agent_presentations
		WHERE project_id = ?
	`, env.projectID).Scan(&independentAgentFiles, &independentAgentEffects); err != nil {
		t.Fatalf("independent agent presentation count: %v", err)
	}

	if len(walk1.Files) != independentTrunkFiles || independentTrunkFiles != 3 {
		t.Fatalf("marked files = %d, independent trunk files = %d (want 3)", len(walk1.Files), independentTrunkFiles)
	}
	if independentAgentFiles != 2 || independentAgentEffects != 3 {
		t.Fatalf("unpresented agent files=%d effects=%d, want 2 files / 3 effects", independentAgentFiles, independentAgentEffects)
	}

	// Open each marked file and verify it reaches the exact comparison that marked it.
	expectedAfter := map[string]string{
		"ai_a.go":  "package a\n// v2\n",
		"ai_b.go":  "package b\n",
		"ext_c.go": "package c\n",
	}
	for _, f := range walk1.Files {
		if f.Path == "worker_only.go" {
			t.Fatalf("worker branch file leaked into trunk Files marks")
		}
		comp, err := env.ledger.Comparisons.CompareScope(
			ctx, env.projectID, sourcebranch.Trunk,
			sourceledger.Baseline{Kind: sourceledger.BaselinePresentation},
			f.FileID, sourceledger.ScopeComparisonOptions{},
		)
		if err != nil {
			t.Fatalf("CompareScope(%s): %v", f.Path, err)
		}
		if !comp.InRange {
			t.Fatalf("marked file %s did not open an in-range comparison", f.Path)
		}
		if want, ok := expectedAfter[f.Path]; ok && comp.After.Content != want {
			t.Fatalf("comparison for %s after=%q want %q", f.Path, comp.After.Content, want)
		}
	}

	// Acknowledge ai_a.go via CompletePresentation -> verify unpresented agent count drops by 1 file (2 effects).
	var aiA sourceledger.WalkFile
	for _, f := range walk1.Files {
		if f.Path == "ai_a.go" {
			aiA = f
			break
		}
	}
	if aiA.FileID == "" || aiA.PresentationEffectID == "" {
		t.Fatalf("ai_a.go missing presentation metadata: %+v", aiA)
	}
	var aiAOrdinal int64
	if err := ro.QueryRowContext(ctx, `SELECT ordinal FROM source_effects WHERE id = ?`, aiA.PresentationEffectID).Scan(&aiAOrdinal); err != nil {
		t.Fatalf("read ai_a ordinal: %v", err)
	}
	if err := env.ledger.Checkpoints.CompletePresentation(ctx, env.projectID, aiA.FileID, aiA.PresentationEffectID, aiAOrdinal); err != nil {
		t.Fatalf("CompletePresentation: %v", err)
	}

	walkAfterLook, err := env.ledger.Walk.QueryWalk(ctx, env.projectID, sourceledger.Baseline{Kind: sourceledger.BaselinePresentation}, 100, 0, sourceledger.CommitLens{})
	if err != nil {
		t.Fatalf("QueryWalk after look: %v", err)
	}
	if len(walkAfterLook.Files) != 2 {
		t.Fatalf("marked files after look = %d, want 2", len(walkAfterLook.Files))
	}
	var remainingAgentFiles, remainingAgentEffects int
	if err := ro.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT file_id), COUNT(*) FROM source_agent_presentations WHERE project_id = ?
	`, env.projectID).Scan(&remainingAgentFiles, &remainingAgentEffects); err != nil {
		t.Fatalf("remaining agent count: %v", err)
	}
	if remainingAgentFiles != 1 || remainingAgentEffects != 1 {
		t.Fatalf("remaining agent files=%d effects=%d, want 1 / 1", remainingAgentFiles, remainingAgentEffects)
	}

	// Withdraw presentation -> mark and agent count return cleanly.
	if err := env.ledger.Checkpoints.WithdrawPresentation(ctx, env.projectID, aiA.FileID, aiAOrdinal); err != nil {
		t.Fatalf("WithdrawPresentation: %v", err)
	}
	walkAfterWithdraw, err := env.ledger.Walk.QueryWalk(ctx, env.projectID, sourceledger.Baseline{Kind: sourceledger.BaselinePresentation}, 100, 0, sourceledger.CommitLens{})
	if err != nil {
		t.Fatalf("QueryWalk after withdraw: %v", err)
	}
	if len(walkAfterWithdraw.Files) != 3 {
		t.Fatalf("marked files after withdraw = %d, want 3", len(walkAfterWithdraw.Files))
	}

	env.logReceipt(t, "E07", map[string]any{
		"initial_marked_files":      len(walk1.Files),
		"independent_trunk_files":   independentTrunkFiles,
		"initial_agent_files":       independentAgentFiles,
		"initial_agent_effects":     independentAgentEffects,
		"post_look_marked_files":    len(walkAfterLook.Files),
		"post_withdraw_marked_files": len(walkAfterWithdraw.Files),
		"worker_branch_isolated":    true,
	})
}

// TestWEATB06E09OutsideProjectAndWorktrees exercises E09:
// - Second registered checkout (git worktree) of the same repository attached as a root:
//   editing the same relative path in each checkout lands in the addressed checkout only
//   and records that checkout's distinct root_id and branch_id in SQLite.
// - Out-of-root target without a grant is refused by sandbox.Boundary (`ProjectRootRequired`),
//   leaving host bytes untouched (SHA-256) and granting no reusable write permission.
func TestWEATB06E09OutsideProjectAndWorktrees(t *testing.T) {
	env := newTB06Env(t, true)
	ctx := env.callerCtx(t)

	// Create a second registered checkout of the same repo.
	wtDir := filepath.Join(t.TempDir(), "checkout-2")
	tb06RunGit(t, env.projectDir, "worktree", "add", "-q", "-b", "tb06-e09-wt", wtDir)
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", env.projectDir, "worktree", "remove", "--force", wtDir).Run()
	})
	wtRoot, err := env.projects.AttachRoot(ctx, env.projectID, project.AttachRootParams{
		Path:  wtDir,
		Label: "checkout-2",
	})
	if err != nil {
		t.Fatalf("AttachRoot checkout-2: %v", err)
	}
	wtID := "wt-e09-" + uuid.NewString()[:8]
	wtBranch := sourcebranch.ForWorktree(wtID)
	nowStr := timelayout.Format(time.Now().UTC())
	if _, err := env.store.ExecContext(ctx, `
		INSERT INTO source_worktrees (id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at)
		VALUES (?, ?, 'repo-e09', ?, ?, 'tb06-e09-wt', 'main', ?)
	`, wtID, env.projectID, env.projectDir, wtDir, nowStr); err != nil {
		t.Fatalf("insert source_worktrees: %v", err)
	}

	// Write same relative path `shared.txt` in trunk and in checkout-2 with distinct content.
	trunkBytes := []byte("trunk-checkout-content\n")
	wtBytes := []byte("second-checkout-content\n")
	if err := os.WriteFile(filepath.Join(env.projectDir, "shared.txt"), trunkBytes, 0o644); err != nil {
		t.Fatalf("write trunk shared.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "shared.txt"), wtBytes, 0o644); err != nil {
		t.Fatalf("write wt shared.txt: %v", err)
	}
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: env.rootID, Path: "shared.txt"},
		ProjectID:      env.projectID, BranchID: sourcebranch.Trunk,
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: trunkBytes,
	}); err != nil {
		t.Fatalf("record trunk shared.txt: %v", err)
	}
	if err := env.ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: wtRoot.Added.ID, Path: "shared.txt"},
		ProjectID:      env.projectID, BranchID: wtBranch,
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: wtBytes,
	}); err != nil {
		t.Fatalf("record wt shared.txt: %v", err)
	}

	// Verify disk isolation by SHA-256.
	trunkDiskSHA, ok1 := tb06SHA256File(t, filepath.Join(env.projectDir, "shared.txt"))
	wtDiskSHA, ok2 := tb06SHA256File(t, filepath.Join(wtDir, "shared.txt"))
	if !ok1 || trunkDiskSHA != tb06SHA256Bytes(trunkBytes) {
		t.Fatalf("trunk shared.txt sha mismatch: %s", trunkDiskSHA)
	}
	if !ok2 || wtDiskSHA != tb06SHA256Bytes(wtBytes) {
		t.Fatalf("checkout-2 shared.txt sha mismatch: %s", wtDiskSHA)
	}
	if bytes.Equal(trunkBytes, wtBytes) || trunkDiskSHA == wtDiskSHA {
		t.Fatalf("test setup error: trunk and worktree bytes must differ")
	}

	// Verify independent SQLite attribution of root_id and branch_id for both checkouts.
	ro, err := db.OpenReadOnly(ctx, env.dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	var gotTrunkSHA, gotWtSHA string
	var gotTrunkBranch, gotWtBranch string
	if err := ro.QueryRowContext(ctx, `
		SELECT v.content_sha256, o.branch_id
		FROM source_versions v JOIN source_operations o ON o.id = v.operation_id
		WHERE v.project_id = ? AND v.root_id = ? AND v.path = 'shared.txt'
		ORDER BY v.seq DESC LIMIT 1
	`, env.projectID, env.rootID).Scan(&gotTrunkSHA, &gotTrunkBranch); err != nil {
		t.Fatalf("query trunk shared.txt RO: %v", err)
	}
	if err := ro.QueryRowContext(ctx, `
		SELECT v.content_sha256, o.branch_id
		FROM source_versions v JOIN source_operations o ON o.id = v.operation_id
		WHERE v.project_id = ? AND v.root_id = ? AND v.path = 'shared.txt'
		ORDER BY v.seq DESC LIMIT 1
	`, env.projectID, wtRoot.Added.ID).Scan(&gotWtSHA, &gotWtBranch); err != nil {
		t.Fatalf("query wt shared.txt RO: %v", err)
	}
	if gotTrunkSHA != trunkDiskSHA || gotTrunkBranch != "" {
		t.Fatalf("trunk record mismatch: sha=%s branch=%q", gotTrunkSHA, gotTrunkBranch)
	}
	if gotWtSHA != wtDiskSHA || gotWtBranch != wtBranch.String() {
		t.Fatalf("worktree record mismatch: sha=%s branch=%q want_branch=%q", gotWtSHA, gotWtBranch, wtBranch.String())
	}

	// Out-of-root target without a grant MUST be refused by sandbox.Boundary and never gain reusable permission.
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	outsideOrig := []byte("outside-untouched\n")
	if err := os.WriteFile(outsideFile, outsideOrig, 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	preOutsideSHA, _ := tb06SHA256File(t, outsideFile)

	boundary := sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, nil)
	if err := boundary.AssertPathAllowed(ctx, env.projectDir, outsideFile, sandbox.PathOpWrite); err == nil {
		t.Fatalf("expected out-of-root path to be refused by sandbox.Boundary")
	}
	// Second check confirms preparing/checking an out-of-root path grants no reusable write permission.
	if err := boundary.AssertPathAllowed(ctx, env.projectDir, outsideFile, sandbox.PathOpWrite); err == nil {
		t.Fatalf("expected repeated out-of-root check to remain refused")
	}
	postOutsideSHA, ok := tb06SHA256File(t, outsideFile)
	if !ok || postOutsideSHA != preOutsideSHA {
		t.Fatalf("outside file mutated during refusal: pre=%s post=%s", preOutsideSHA, postOutsideSHA)
	}
	// Adjacent positive control: inside attached worktree root succeeds.
	if err := boundary.AssertPathAllowed(ctx, wtDir, "shared.txt", sandbox.PathOpWrite); err != nil {
		t.Fatalf("expected inside-worktree path to be admitted, got %v", err)
	}

	var unused sql.NullString
	_ = unused
	env.logReceipt(t, "E09", map[string]any{
		"trunk_sha256":             trunkDiskSHA,
		"worktree_sha256":          wtDiskSHA,
		"worktree_branch":          wtBranch.String(),
		"outside_refused_twice":    true,
		"outside_file_unchanged":   preOutsideSHA == postOutsideSHA,
		"inside_worktree_admitted": true,
	})
}
