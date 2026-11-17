package history

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// countingSummarizer counts provider summaries; background passes call it from
// another goroutine.
type countingSummarizer struct{ calls atomic.Int32 }

func (s *countingSummarizer) Summarize(ctx context.Context, system, user string, maxTokens int) (string, error) {
	s.calls.Add(1)
	return compaction.MockSummarizer{Text: "Continue from compacted context."}.Summarize(ctx, system, user, maxTokens)
}

// oversizedSession holds history above the hard ceiling, so a request fit
// schedules compaction and a background page publishes a view.
func oversizedSession(t *testing.T, summarizer compaction.Summarizer) (*Service, *store.Memory, *api.Session) {
	t.Helper()
	m, st := newCompactionManager(t, compaction.DefaultCompactionConfig())
	m.SetCompactor(compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), summarizer))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	var rows []api.Message
	for i, id := range []string{"a", "b", "c", "d", "e", "f"} {
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		rows = append(rows, api.Message{ID: id, Role: role, Content: strings.Repeat("Observed evidence line "+id+".\n", 4000)})
	}
	rows = append(rows, api.Message{ID: "recent", Role: api.MessageRoleUser, Content: "Continue"})
	testutil.FailErr(t, "append history", st.AppendMessages(t.Context(), sess.ID, rows...))
	sess, err = st.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "reload session", err)
	return m, st, sess
}

// holdCompaction admits a manual pass so no background pass for the session
// runs until the returned release is called.
func holdCompaction(t *testing.T, m *Service, sessionID string) func() {
	t.Helper()
	entered, released, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- m.Runner.Execute(context.Background(), sessionID, func(context.Context) error {
			close(entered)
			<-released
			return nil
		})
	}()
	<-entered
	return func() {
		close(released)
		testutil.FailErr(t, "held compaction pass", <-done)
	}
}

func requirePublishedView(t *testing.T, st *store.Memory, sessionID string) {
	t.Helper()
	_, ok, err := st.GetCompactionView(t.Context(), sessionID)
	testutil.FailErr(t, "read compaction view", err)
	if !ok {
		t.Fatal("background compaction published no view")
	}
}

// Fitting a request never calls the provider summarizer; it only schedules the
// background pass that does.
func TestCompactionSyncPathAvoidsSummarizer(t *testing.T) {
	summarizer := &countingSummarizer{}
	m, st, sess := oversizedSession(t, summarizer)
	release := holdCompaction(t, m, sess.ID)
	rows, err := st.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read history", err)
	_, _, err = m.Fit(t.Context(), sess, rows, "")
	testutil.FailErr(t, "fit request", err)
	if got := summarizer.calls.Load(); got != 0 {
		t.Fatalf("request fit called the summarizer %d times", got)
	}
	release()
	m.Runner.Wait()
	requirePublishedView(t, st, sess.ID)
}

// A scheduled pass outlives the request that scheduled it.
func TestScheduledCompactionPublishesAfterTheRequestIsCanceled(t *testing.T) {
	m, st, sess := oversizedSession(t, &countingSummarizer{})
	release := holdCompaction(t, m, sess.ID)
	request, cancel := context.WithCancel(t.Context())
	m.Schedule(request, sess)
	cancel()
	release()
	m.Runner.Wait()
	requirePublishedView(t, st, sess.ID)
}
