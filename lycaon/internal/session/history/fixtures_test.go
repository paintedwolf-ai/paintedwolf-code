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
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newCompactionManager(t *testing.T, cfg compaction.CompactionConfig) (*Service, *store.Memory) {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	data := store.NewMemory()
	workspace := sessionscope.New(data)
	m := New(data, lifecycle.New(data), workspace, limits.New(settings.DefaultSessionLimits(), workspace), nil, func(context.Context, *api.Session) bool { return false })
	m.SetDataDir(t.TempDir())
	m.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "Continue from compacted context."}))
	t.Cleanup(m.Wait)
	return m, data
}
func appliedView(t *testing.T, m *Service, data Store, id string) []api.Message {
	t.Helper()
	ctx := context.Background()
	sess, err := data.Get(ctx, id)
	testutil.FailErr(t, "get session", err)
	rows, err := data.GetMessages(ctx, id)
	testutil.FailErr(t, "get messages", err)
	return m.ApplyView(ctx, sess, rows)
}
