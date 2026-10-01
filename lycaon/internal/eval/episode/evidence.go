package episode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
)

const Version = 1

// Evidence contains observations from one closed application session tree.
type Evidence struct {
	RootSessionID  string              `json:"root_session_id"`
	Snapshots      map[string]Snapshot `json:"snapshots"`
	DeliveredPaths []string            `json:"delivered_paths"`
	Version        int                 `json:"version"`
	ProjectID      string              `json:"project_id"`
	Execution      *Execution          `json:"execution"`
	Allowance      *store.ModelLimit   `json:"allowance"`
	Sessions       []Session           `json:"sessions"`
	Workers        []Worker            `json:"workers"`
	Verification   []evidence.Record   `json:"verification"`
	Effects        []SourceEffect      `json:"effects"`
	Promotions     map[string]string   `json:"promotions"`
}

type Session struct {
	ID          string                    `json:"id"`
	ParentID    string                    `json:"parent_id"`
	Messages    []api.Message             `json:"messages"`
	Invocations []api.InvocationReceipt   `json:"invocations"`
	Checkpoints []api.CheckpointEvent     `json:"checkpoints"`
	Verdicts    []workflow.VerdictReceipt `json:"verdicts"`
	Workflows   []api.WorkflowRun         `json:"workflows"`
}

type Worker struct {
	api.WorkerTask
	SourceToolCallID string `json:"source_tool_call_id"`
}

// Read uses the same repositories as the running application, without starting it.
func Read(ctx context.Context, capture, sessionID string) (Evidence, error) {
	result := Evidence{RootSessionID: sessionID, Version: Version, Sessions: []Session{}, Workers: []Worker{}}
	database, err := openCapture(ctx, capture)
	if err != nil {
		return result, err
	}
	defer func() { _ = database.Close() }()
	queries := db.New(database)
	root, err := queries.GetSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	result.ProjectID = root.ProjectID
	if err := result.readExecution(ctx, database, sessionID); err != nil {
		return result, err
	}
	ids, err := queries.ListSessionTreeIDs(ctx, sessionID)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		member, err := readSession(ctx, database, id, root.ProjectID)
		if err != nil {
			return result, fmt.Errorf("session %s: %w", id, err)
		}
		result.Sessions = append(result.Sessions, member)
	}
	tasks, err := worker.NewSQLStore(database).ListBySession(ctx, root.ProjectID, sessionID)
	if err != nil {
		return result, err
	}
	for _, task := range tasks {
		scope := task.EffectiveScope()
		task.Scope = &scope
		result.Workers = append(result.Workers, Worker{WorkerTask: task, SourceToolCallID: task.SourceToolCallID})
	}
	result.Effects, err = readEffects(ctx, database, root.ProjectID)
	if err != nil {
		return result, err
	}
	result.Promotions, err = readPromotions(ctx, database, root.ProjectID)
	if err != nil {
		return result, err
	}
	result.Verification, err = inspector.NewJSONLStore("evidence").ReadAll(ctx, filepath.Join(capture, "projects", root.ProjectID), sessionID, "tests", evidence.GateTypeVerify)
	return result, err
}

func (e *Evidence) readExecution(ctx context.Context, database *sql.DB, sessionID string) error {
	execution, err := ReadExecution(ctx, database, sessionID)
	if err == nil {
		e.Execution = &execution
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	allowance, err := ReadAllowance(ctx, database, sessionID)
	if err == nil {
		e.Allowance = &allowance
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func readSession(ctx context.Context, database *sql.DB, id, projectID string) (Session, error) {
	result := Session{ID: id}
	queries := db.New(database)
	row, err := queries.GetSession(ctx, id)
	if err != nil {
		return result, err
	}
	if row.ProjectID != projectID {
		return result, fmt.Errorf("session tree crosses project boundary")
	}
	result.ParentID = row.ParentSessionID.String
	result.Messages, err = store.ReadMessages(ctx, queries, id)
	if err != nil {
		return result, err
	}
	result.Invocations, err = invocation.NewSQLRecorder(database).ListSession(ctx, id)
	if err != nil {
		return result, err
	}
	result.Checkpoints, result.Workflows, err = readBoundaries(ctx, database, id)
	if err != nil {
		return result, err
	}
	result.Verdicts = []workflow.VerdictReceipt{}
	for _, run := range result.Workflows {
		receipts, err := workflow.NewSQLStore(database).ReadVerdictReceipts(ctx, run.ID)
		if err != nil {
			return result, err
		}
		result.Verdicts = append(result.Verdicts, receipts...)
	}
	return result, nil
}
