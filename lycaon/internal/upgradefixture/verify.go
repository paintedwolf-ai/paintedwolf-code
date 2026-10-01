package upgradefixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

type WorkerEvidence struct {
	JobID           string            `json:"job_id"`
	ParentSessionID string            `json:"parent_session_id"`
	ChildSessionID  string            `json:"child_session_id"`
	Baseline        map[string]string `json:"baseline"`
	Overlay         map[string]string `json:"overlay"`
}

type Manifest struct {
	Editor    EditorEvidence `json:"editor_history"`
	ProjectID string         `json:"project_id"`
	SessionID string         `json:"session_id"`
	Source    SourceEvidence `json:"source_history"`
	Worker    WorkerEvidence `json:"worker_history"`
}

// Verify reads the installed history and reconstructs a worker in disposable storage.
func Verify(ctx context.Context, dataDir, manifestPath string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	database, err := db.OpenReadOnly(ctx, filepath.Join(dataDir, "store.db"))
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	if err := VerifySourceHistory(ctx, database, dataDir, manifest.ProjectID, manifest.SessionID, manifest.Source); err != nil {
		return err
	}
	if err := VerifyEditorHistory(ctx, database, dataDir, manifest.ProjectID, manifest.Editor); err != nil {
		return err
	}
	return VerifyWorkerHistory(ctx, database, manifest.ProjectID, manifest.Worker)
}

func VerifyWorkerHistory(ctx context.Context, database db.Handle, projectID string, evidence WorkerEvidence) error {
	if evidence.JobID == "" || evidence.ParentSessionID == "" || evidence.ChildSessionID == "" || len(evidence.Baseline) == 0 || len(evidence.Overlay) == 0 {
		return fmt.Errorf("worker fixture evidence is incomplete")
	}
	tasks, err := worker.NewSQLStore(database).List(ctx, projectID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.ID != evidence.JobID {
			continue
		}
		if task.Status != api.WorkerStatusComplete || task.ParentSessionID != evidence.ParentSessionID || task.ChildSessionID != evidence.ChildSessionID || task.WorkspaceBaselinePath == "" || task.WorkspaceOverlayPath == "" {
			return fmt.Errorf("retained worker lost completed history")
		}
		return verifyWorkerBodies(ctx, &task, evidence)
	}
	return fmt.Errorf("retained worker %s is missing", evidence.JobID)
}

func verifyWorkerBodies(ctx context.Context, task *api.WorkerTask, evidence WorkerEvidence) error {
	if err := verifyManifestBodies(ctx, task.WorkspaceBaselinePath, evidence.Baseline); err != nil {
		return err
	}
	if err := verifyManifestBodies(ctx, task.WorkspaceOverlayPath, evidence.Overlay); err != nil {
		return err
	}
	roots := worker.TaskRootRefs(ctx, task, nil)
	if len(roots) != 1 {
		return fmt.Errorf("retained worker topology is missing")
	}
	branch, err := os.MkdirTemp("", "upgrade-worker-reconstruction-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(branch) }()
	report, err := workspacebaseline.Materialize(ctx, task.WorkspaceBaselinePath, task.WorkspaceOverlayPath, workspacebaseline.ContentStore(task.WorkspaceBaselinePath), roots, branch, nil)
	if err != nil {
		return err
	}
	if len(report.Unavailable) != 0 {
		return fmt.Errorf("retained worker reconstruction lacks %v", report.Unavailable)
	}
	for path, expected := range evidence.Overlay {
		if !filepath.IsLocal(path) {
			return fmt.Errorf("invalid worker fixture path")
		}
		body, err := os.ReadFile(filepath.Join(branch, path))
		if err != nil {
			return err
		}
		if digest(body) != expected {
			return fmt.Errorf("reconstructed worker body changed: %s", path)
		}
	}
	return nil
}

func verifyManifestBodies(ctx context.Context, path string, expected map[string]string) error {
	reader, err := workspacebaseline.Open(ctx, path, workspacebaseline.ContentStore(path))
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for name, sha := range expected {
		body, present, err := reader.Content(ctx, name)
		if err != nil {
			return err
		}
		if !present || digest([]byte(body)) != sha {
			return fmt.Errorf("retained worker body changed: %s", name)
		}
	}
	return nil
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
