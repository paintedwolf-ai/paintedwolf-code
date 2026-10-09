package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type failedCompactionSummarizer struct{}

func (failedCompactionSummarizer) Summarize(context.Context, string, string, int) (string, error) {
	return "", errors.New("local summarizer unavailable")
}

func TestFailedCompactionPersistsNothingAndRetriesFromCanonicalHistory(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 1000
	cfg.BudgetTriggerPct = 50
	cfg.HardCeilingTokens = 500
	cfg.TargetTokens = 300
	cfg.ChunkTokenThreshold = 1_000_000
	cfg.KeepRecentMessages = 2

	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, failedCompactionSummarizer{}))
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("retained fact ", 120)
	seed := make([]api.Message, 0, 20)
	for i := 0; i < 20; i++ {
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		seed = append(seed, api.Message{ID: fmt.Sprintf("seed-%d", i), Role: role, Content: block})
	}
	testutil.FailErr(t, "append canonical history", mem.AppendMessages(t.Context(), sess.ID, seed...))
	mgr.Runner.History.ObserveTokens(sess.ID, 1000, 1000)

	_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "first attempt")
	testutil.FailErr(t, "first prompt", err)
	mgr.Runner.History.Wait()
	got, err := mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session after failure", err)
	if got.CompactionGeneration != 0 {
		t.Fatalf("failed compaction advanced generation to %d", got.CompactionGeneration)
	}
	if _, ok, viewErr := mem.GetCompactionView(t.Context(), sess.ID); viewErr != nil || ok {
		t.Fatalf("failed compaction persisted a view: ok=%v err=%v", ok, viewErr)
	}

	// Prompt fitting does not lower the durable retry threshold.
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "validated continuation"}))
	_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "retry")
	testutil.FailErr(t, "retry prompt", err)
	mgr.Runner.History.Wait()
	got, err = mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session after retry", err)
	if got.CompactionGeneration == 0 {
		t.Fatal("successful retry did not persist a compaction view")
	}
	if _, ok, viewErr := mem.GetCompactionView(t.Context(), sess.ID); viewErr != nil || !ok {
		t.Fatalf("successful retry view: ok=%v err=%v", ok, viewErr)
	}
}
