package history

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/limits"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newCompactionManager(t *testing.T, cfg compaction.CompactionConfig) (*Service, *store.Repositories) {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	data := storetest.New(t, testdbseed.DefaultProjectID)
	workspace := sessionscope.New(data.Sessions)
	m := New(data.Sessions, data.Transcript, data.Compaction, lifecycle.New(data.Sessions), workspace, limits.New(settings.DefaultSessionLimits(), workspace), nil, func(context.Context, *api.Session) bool { return false })
	m.SetDataDir(t.TempDir())
	m.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "Continue from compacted context."}))
	t.Cleanup(m.Wait)
	return m, data
}
func appliedView(t *testing.T, m *Service, sessionStore Sessions, transcriptStore Transcript, compactionStore Compaction, id string) []api.Message {
	t.Helper()
	ctx := context.Background()
	sess, err := sessionStore.Get(ctx, id)
	testutil.FailErr(t, "get session", err)
	rows, err := transcriptStore.GetMessages(ctx, id)
	testutil.FailErr(t, "get messages", err)
	return m.ApplyView(ctx, sess, rows)
}
