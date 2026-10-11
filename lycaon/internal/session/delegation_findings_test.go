package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerChildProjectDirIsDelegationNotOverlay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-write")

	store := store.NewMemory()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer", Prompt: "write leg"})
	testutil.FailErr(t, "create child", err)

	if child.WorkspacePath != parent.WorkspacePath {
		t.Fatalf("child ProjectDir = %q parent = %q", child.WorkspacePath, parent.WorkspacePath)
	}
	if child.WorkspacePath == overlay {
		t.Fatal("child ProjectDir must not be the overlay root")
	}
}

func TestRecentSiblingNotesExcludesPriorRunFindings(t *testing.T) {
	transcript := store.NewMemory()
	parent, err := transcript.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	root := parent.ID
	testutil.FailErr(t, "record human intent", transcript.AppendMessages(t.Context(), root, api.Message{ID: "intent", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "build modules", CreatedAt: time.Now().UTC().Add(-time.Hour)}))
	sqlDB := testdbfixture.Open(t, "findings.db")
	findingsStore := findings.NewSQLStore(sqlDB)
	testdbseed.InsertSession(t, sqlDB, root, testdbseed.DefaultProjectID)

	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	_, err = sqlDB.ExecContext(t.Context(),
		`INSERT INTO findings (session_id, agent, summary, ref, created_at) VALUES (?, ?, ?, ?, ?)`,
		root, "old-job", "prior run finding", "old.go", oldTime.Format(time.RFC3339),
	)
	testutil.FailErr(t, "insert old finding", err)

	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO findings(session_id,agent,summary,ref,created_at) VALUES (?,?,?,?,?)`, root, "job-before", "contract before spawn", "module.go", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339))
	testutil.FailErr(t, "insert current batch contract", err)
	spawnAt := time.Now().UTC()
	m := workeroutcomes.NewNotes(transcript, findingsStore, &stubSiblingTaskQueue{byChild: map[string]*api.WorkerTask{root: {ID: "job-b", CreatedAt: spawnAt}}}, nil)

	ctx := workercontext.WithJob(context.Background(), "job-b")
	notes, _, err := m.RecentSiblingNotes(ctx, root, 0, 5)
	testutil.FailErr(t, "load prior notes", err)
	if len(notes) != 1 || notes[0].Summary != "contract before spawn" {
		t.Fatalf("batch notes = %+v want pre-spawn contract only", notes)
	}

	_, err = findingsStore.Append(context.Background(), root, "job-a", "fresh sibling note", "fresh.go", "")
	testutil.FailErr(t, "append finding", err)
	notes2, _, err := m.RecentSiblingNotes(ctx, root, 0, 5)
	testutil.FailErr(t, "load fresh notes", err)
	if len(notes2) != 2 || notes2[1].Summary != "fresh sibling note" {
		t.Fatalf("post-spawn notes = %+v", notes2)
	}
}

type stubSiblingTaskQueue struct {
	noopWorkerBranchClaim
	byChild map[string]*api.WorkerTask
}

func (s *stubSiblingTaskQueue) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func (s *stubSiblingTaskQueue) Get(jobID string) (*api.WorkerTask, bool) {
	for _, task := range s.byChild {
		if task != nil && task.ID == jobID {
			return task, true
		}
	}
	return nil, false
}

func (*stubSiblingTaskQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
