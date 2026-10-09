package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type cancelableCompactionSummary struct{ started chan struct{} }

func (s cancelableCompactionSummary) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	close(s.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestManualCompactionDoesNotHoldSessionStopAdmission(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.KeepRecentMessages = 1
	cfg.ChunkTokenThreshold = 1000000
	cfg.TargetTokens = 100
	mgr, st := newCompactionManager(t, cfg)
	started := make(chan struct{})
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, cancelableCompactionSummary{started: started}))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append history", st.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "old", Role: api.MessageRoleAssistant, Content: strings.Repeat("Old observations. ", 2000)},
		api.Message{ID: "current", Role: api.MessageRoleUser, Content: "Continue."}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := mgr.Runner.History.ForceCompact(ctx, sess.ID); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("summarizer did not start")
	}
	stopEntered := make(chan *lifecycle.Flight, 1)
	go func() { flight, _ := mgr.Gate.Begin(sess.ID); stopEntered <- flight }()
	select {
	case flight := <-stopEntered:
		mgr.Runner.History.Runner.CancelSession(sess.ID)
		mgr.Gate.Finish(sess.ID, flight, nil)
	case <-time.After(5 * time.Second):
		cancel()
		flight := <-stopEntered
		mgr.Gate.Finish(sess.ID, flight, nil)
		t.Fatal("manual compaction blocked stop admission")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("compaction cancellation: %v", err)
	}
	current, err := st.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read generation", err)
	if current.CompactionGeneration != 0 {
		t.Fatal("canceled compaction published a view")
	}
}
