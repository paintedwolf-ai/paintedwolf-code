package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

const (
	workerRunA = "11111111-1111-4111-8111-111111111111"
	workerRunB = "22222222-2222-4222-8222-222222222222"
)

type workerTranscriptStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
	GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error)
	GetTranscriptPage(context.Context, string, api.TranscriptPageQuery) (api.SessionTranscriptPage, error)
}

func TestMemoryWorkerJobTranscriptPagination(t *testing.T) {
	store := NewMemory()
	session, err := store.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	assertWorkerJobTranscriptPagination(t, store, session.ID)
}

func TestSQLWorkerJobTranscriptPagination(t *testing.T) {
	database := testdbfixture.Open(t, "worker-transcript.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	store := NewSQL(database)
	session, err := store.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	for _, jobID := range []string{workerRunA, workerRunB} {
		_, err = database.ExecContext(t.Context(), `
			INSERT INTO worker_jobs (
				id, project_id, child_session_id, agent_type, status, prompt, brief, created_at
			) VALUES (?, ?, ?, 'repo-researcher', 'complete', 'fixture', 'fixture', ?)
		`, jobID, testdbseed.DefaultProjectID, session.ID, time.Now().UTC().Format(time.RFC3339Nano))
		testutil.FailErr(t, "insert worker run", err)
	}
	assertWorkerJobTranscriptPagination(t, store, session.ID)
}

func assertWorkerJobTranscriptPagination(t *testing.T, store workerTranscriptStore, sessionID string) {
	t.Helper()
	if _, err := store.GetWorkerJobMessages(t.Context(), sessionID, ""); err == nil {
		t.Fatal("empty worker job id was accepted")
	}
	for index, jobID := range []string{workerRunA, workerRunB, workerRunA, workerRunB, workerRunA} {
		err := store.AppendMessages(t.Context(), sessionID, api.Message{
			ID:       string(rune('a' + index)),
			Role:     api.MessageRoleAssistant,
			Content:  jobID,
			WorkerID: jobID,
		})
		testutil.FailErr(t, "append worker message", err)
	}
	all, err := store.GetWorkerJobMessages(t.Context(), sessionID, workerRunA)
	testutil.FailErr(t, "load worker transcript", err)
	if got := messageIDs(all); len(got) != 3 || got[0] != "a" || got[1] != "c" || got[2] != "e" {
		t.Fatalf("worker transcript ids = %v", got)
	}

	tail, err := store.GetTranscriptPage(t.Context(), sessionID, api.TranscriptPageQuery{
		Limit: 2, WorkerID: workerRunA,
	})
	testutil.FailErr(t, "load worker tail", err)
	if got := messageIDs(tail.Messages); len(got) != 2 || got[0] != "c" || got[1] != "e" {
		t.Fatalf("tail ids = %v", got)
	}
	if tail.BeforeCursor == "" || tail.AfterCursor != "" {
		t.Fatalf("tail cursors = before:%v after:%v", tail.BeforeCursor, tail.AfterCursor)
	}

	before := tail.Messages[0].Ord
	older, err := store.GetTranscriptPage(t.Context(), sessionID, api.TranscriptPageQuery{
		Before: &before, Limit: 2, WorkerID: workerRunA,
	})
	testutil.FailErr(t, "load older worker page", err)
	if got := messageIDs(older.Messages); len(got) != 1 || got[0] != "a" {
		t.Fatalf("older ids = %v", got)
	}
	if older.BeforeCursor != "" || older.AfterCursor == "" {
		t.Fatalf("older cursors = before:%v after:%v", older.BeforeCursor, older.AfterCursor)
	}
}

func messageIDs(messages []api.Message) []string {
	ids := make([]string, len(messages))
	for index, message := range messages {
		ids[index] = message.ID
	}
	return ids
}
