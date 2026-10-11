package contractfixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

func ArtifactProject(t *testing.T) (*project.MemoryRegistry, string) {
	t.Helper()
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Draft: true, Name: "Artifact fixture"})
	testutil.FailErr(t, "register artifact project", err)
	return registry, p.ID
}

func DurableArtifacts(t *testing.T, dataDir, projectID string, sessionIDs ...string) *visual.DurableStore {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, projectID)
	for _, id := range sessionIDs {
		testdbseed.InsertSession(t, sqlDB, id, projectID)
	}
	return visual.NewDurableStore(visual.DurableConfig{
		ArtifactsDir: func(pid string) (string, error) {
			dir := filepath.Join(dataDir, "projects", pid, "artifacts")
			if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
				return "", mkErr
			}
			return dir, nil
		},
		Lookup:  func(context.Context, string) (string, error) { return projectID, nil },
		Records: visual.NewRecords(sqlDB, eventoutbox.New(sqlDB, nil), visual.ArtifactProjection{}),
	})
}

func RecordingMessage(messageID, toolCallID string) api.Message {
	return api.Message{
		ID: messageID, Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{ID: toolCallID, Name: "page_act"}},
	}
}

func RecordingParams(operationID, messageID, toolCallID string) string {
	return "page_id=page-1&assistant_message_id=" + messageID + "&tool_call_id=" + toolCallID + "&operation_id=" + operationID
}

func RecordingQuery(operationID string) string {
	return "?" + RecordingParams(operationID, "message-1", "call-1")
}

type ResolutionStore struct {
	Res        visual.Resolution
	StorageErr error
}

func (s ResolutionStore) Put(context.Context, string, visual.Entry) (api.VisualArtifact, error) {
	return api.VisualArtifact{}, nil
}

func (s ResolutionStore) Resolve(context.Context, string, string) visual.Resolution { return s.Res }

func (s ResolutionStore) ListProject(context.Context, string, visual.ArtifactPageQuery) (visual.ArtifactPage, error) {
	return visual.ArtifactPage{}, nil
}

func (s ResolutionStore) ListTree(context.Context, string) ([]api.ArtifactListItem, error) {
	return nil, nil
}

func (s ResolutionStore) FindByEvidenceHandle(context.Context, string, string) (string, error) {
	return "", nil
}

func (s ResolutionStore) Delete(context.Context, string, string, string) (visual.ArtifactDeleteResult, bool, error) {
	return visual.ArtifactDeleteResult{}, false, nil
}

func (s ResolutionStore) DeleteGroup(context.Context, string, []string, string) (int64, error) {
	return 0, nil
}

func (s ResolutionStore) CollectGarbage(context.Context) error { return nil }

func (s ResolutionStore) Discard(context.Context, string, string) error { return nil }

func (s ResolutionStore) StorageUsage(context.Context, string) (storageusage.Usage, error) {
	return storageusage.Usage{}, s.StorageErr
}
