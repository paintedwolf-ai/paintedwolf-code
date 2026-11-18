package history

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// countingSummarizer records each provider summary and answers at once;
// background passes call it from another goroutine.
type countingSummarizer struct{ calls atomic.Int32 }

func (s *countingSummarizer) Summarize(ctx context.Context, system, user string, maxTokens int) (string, error) {
	s.calls.Add(1)
	return compaction.MockSummarizer{Text: "Continue from compacted context."}.Summarize(ctx, system, user, maxTokens)
}

// oversizedSession holds history above the hard ceiling, so a request fit
// schedules compaction, a background pass publishes a view and its synthesis
// calls the summarizer.
func oversizedSession(t *testing.T, summarizer compaction.Summarizer) (*Service, *store.SQL, *api.Session) {
	t.Helper()
	m, st := newCompactionManager(t, compaction.DefaultCompactionConfig())
	m.SetCompactor(compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), summarizer))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	// Many rows each below the chunk threshold: chunk reduction cannot bring the
	// session under budget, so the background pass needs a provider summary.
	var rows []api.Message
	for i := range 400 {
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		rows = append(rows, api.Message{ID: fmt.Sprintf("row-%03d", i), Role: role, Content: strings.Repeat(fmt.Sprintf("Observed evidence line %d.\n", i), 60)})
	}
	rows = append(rows, api.Message{ID: "recent", Role: api.MessageRoleUser, Content: "Continue"})
	testutil.FailErr(t, "append history", st.AppendMessages(t.Context(), sess.ID, rows...))
	sess, err = st.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "reload session", err)
	return m, st, sess
}

// holdCompaction admits a manual pass so no background pass for the session
// runs until release is called. Release is also a cleanup registered after the
// service's own, so it runs first: the service's Wait joins the scheduled pass,
// which waits for this one, and a test that stops early must not leave it held.
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
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(released)
			testutil.FailErr(t, "held compaction pass", <-done)
		})
	}
	t.Cleanup(release)
	return release
}

func requirePublishedView(t *testing.T, st *store.SQL, sessionID string) {
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
		t.Errorf("request fit called the summarizer %d times", got)
	}
	release()
	m.Runner.Wait()
	requirePublishedView(t, st, sess.ID)
	// Provider summaries come from the synthesis the scheduled pass starts.
	testutil.FailErr(t, "wait for synthesis", m.Runner.Synthesis.WaitContext(t.Context()))
	if summarizer.calls.Load() == 0 {
		t.Fatal("the scheduled work never summarized, so the request-path check proved nothing")
	}
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
