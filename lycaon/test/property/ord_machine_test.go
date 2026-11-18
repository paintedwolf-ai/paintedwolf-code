package property

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

// ordEntry is the model's record of one stored message.
type ordEntry struct {
	id      string
	ord     int64
	created time.Time
	content string
}

// ordMachine drives one session through every transcript write and holds the
// model the store must match after each: ords are minted once, increase, and
// are never reused, whatever was rewound.
type ordMachine struct {
	ctx     context.Context
	store   *store.SQL
	session string
	entries []ordEntry
	// high is the largest ord ever minted in the session.
	high int64
	seq  map[string]int64
}

func (m *ordMachine) draft(t *rapid.T, label string) api.Message {
	msg := api.Message{
		ID:      uuid.NewString(),
		Role:    rapid.SampledFrom([]api.MessageRole{api.MessageRoleUser, api.MessageRoleAssistant, api.MessageRoleTool, api.MessageRoleSystem}).Draw(t, label+"_role"),
		Content: rapid.StringMatching(`[a-z ]{1,20}`).Draw(t, label+"_content"),
	}
	// A creation time out of order with ord must not reorder the transcript.
	if rapid.Bool().Draw(t, label+"_stamped") {
		msg.CreatedAt = time.Date(2026, 7, 8, 12, 0, rapid.IntRange(0, 59).Draw(t, label+"_second"), 0, time.UTC)
	}
	return msg
}

func (m *ordMachine) appendBatch(t *rapid.T, count int) {
	batch := make([]api.Message, count)
	for i := range batch {
		batch[i] = m.draft(t, "batch")
	}
	failErr(t, "append", m.store.AppendMessages(m.ctx, m.session, batch...))
	stored := m.read(t)
	var shared time.Time
	for i, msg := range batch {
		m.high++
		got := stored[len(stored)-count+i]
		if got.ID != msg.ID || got.Ord != m.high {
			t.Fatalf("batch message %d stored as %s ord %d, want %s ord %d", i, got.ID, got.Ord, msg.ID, m.high)
		}
		created := msg.CreatedAt
		if created.IsZero() {
			// One append stamps every unstamped message with one time.
			if shared.IsZero() {
				shared = got.CreatedAt
			}
			created = shared
		}
		m.entries = append(m.entries, ordEntry{id: msg.ID, ord: m.high, created: created, content: msg.Content})
	}
}

func (m *ordMachine) Append(t *rapid.T) { m.appendBatch(t, 1) }

func (m *ordMachine) Batch(t *rapid.T) { m.appendBatch(t, rapid.IntRange(2, 4).Draw(t, "batch_size")) }

func (m *ordMachine) Patch(t *rapid.T) {
	if len(m.entries) == 0 {
		t.Skip("nothing to patch")
	}
	i := rapid.IntRange(0, len(m.entries)-1).Draw(t, "patch_index")
	patch := m.draft(t, "patch")
	patch.ID = m.entries[i].id
	// Patches carry stale or forged creation fields; the store keeps its own.
	patch.Ord = rapid.Int64Range(0, m.high+5).Draw(t, "patch_ord")
	patch.Seq = rapid.Int64Range(0, 5).Draw(t, "patch_seq")
	patch.DraftStatus = rapid.SampledFrom(append([]api.DraftStatus{""}, api.AllDraftStatuses()...)).Draw(t, "patch_draft_status")
	_, err := m.store.UpdateMessage(m.ctx, m.session, patch.ID, patch)
	failErr(t, "patch", err)
	m.entries[i].content = patch.Content
}

func (m *ordMachine) Rewind(t *rapid.T) {
	if len(m.entries) == 0 {
		t.Skip("nothing to rewind")
	}
	i := rapid.IntRange(0, len(m.entries)-1).Draw(t, "rewind_index")
	anchor := m.entries[i].id
	op := uuid.NewString()
	failErr(t, "prepare rewind", m.store.PrepareRewind(m.ctx, store.RewindOperation{
		ID: op, SessionID: m.session, AnchorMessageID: anchor, InputDigest: "digest", ProjectDir: "/project",
		CheckpointAnchorIDs: []string{anchor},
	}))
	failErr(t, "rewind applying", m.store.SetRewindPhase(m.ctx, op, "applying", ""))
	failErr(t, "rewind files applied", m.store.SetRewindPhase(m.ctx, op, "files_applied", ""))
	_, err := m.store.CommitRewind(m.ctx, op, m.session, anchor, api.RewindSessionResponse{})
	failErr(t, "commit rewind", err)
	m.entries = m.entries[:i]
}

func (m *ordMachine) DuplicateID(t *rapid.T) {
	if len(m.entries) == 0 {
		t.Skip("no id to repeat")
	}
	again := m.draft(t, "duplicate")
	again.ID = m.entries[rapid.IntRange(0, len(m.entries)-1).Draw(t, "duplicate_index")].id
	err := m.store.AppendMessages(m.ctx, m.session, m.draft(t, "duplicate_lead"), again)
	if !errors.Is(err, store.ErrDuplicateMessageID) {
		t.Fatalf("append repeating %s = %v, want ErrDuplicateMessageID", again.ID, err)
	}
}

// ConcurrentAppend runs two appenders at once; each keeps its own order and
// together they take the next ords without a gap or a repeat.
func (m *ordMachine) ConcurrentAppend(t *rapid.T) {
	count := rapid.IntRange(1, 4).Draw(t, "concurrent_count")
	lanes := [2][]api.Message{}
	for lane := range lanes {
		for range count {
			lanes[lane] = append(lanes[lane], m.draft(t, "concurrent"))
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, len(lanes))
	for lane, msgs := range lanes {
		wg.Go(func() {
			for _, msg := range msgs {
				if err := m.store.AppendMessages(m.ctx, m.session, msg); err != nil {
					errs[lane] = err
					return
				}
			}
		})
	}
	wg.Wait()
	failErr(t, "concurrent append", errors.Join(errs...))
	stored := m.read(t)
	tail := stored[len(stored)-2*count:]
	position := map[string]int{}
	for i, got := range tail {
		if got.Ord != m.high+int64(i)+1 {
			t.Fatalf("concurrent append ord %d at position %d, want %d", got.Ord, i, m.high+int64(i)+1)
		}
		position[got.ID] = i
	}
	for _, msgs := range lanes {
		for i := 1; i < len(msgs); i++ {
			if position[msgs[i].ID] <= position[msgs[i-1].ID] {
				t.Fatalf("appender order lost: %s stored before %s", msgs[i].ID, msgs[i-1].ID)
			}
		}
	}
	for _, got := range tail {
		m.entries = append(m.entries, ordEntry{id: got.ID, ord: got.Ord, created: got.CreatedAt, content: got.Content})
	}
	m.high += int64(2 * count)
}

func (m *ordMachine) read(t *rapid.T) []api.Message {
	stored, err := m.store.GetMessages(m.ctx, m.session)
	failErr(t, "read transcript", err)
	return stored
}

func (m *ordMachine) Check(t *rapid.T) {
	stored := m.read(t)
	if len(stored) != len(m.entries) {
		t.Fatalf("transcript holds %d messages, model %d", len(stored), len(m.entries))
	}
	for i, got := range stored {
		want := m.entries[i]
		if got.ID != want.id || got.Ord != want.ord || !got.CreatedAt.Equal(want.created) || got.Content != want.content {
			t.Fatalf("message %d = %s ord %d created %v %q, want %s ord %d created %v %q",
				i, got.ID, got.Ord, got.CreatedAt, got.Content, want.id, want.ord, want.created, want.content)
		}
		// seq is the mutation clock: every write moves it forward.
		if prev, seen := m.seq[got.ID]; seen && got.Seq < prev {
			t.Fatalf("message %s seq went back from %d to %d", got.ID, prev, got.Seq)
		}
		m.seq[got.ID] = got.Seq
	}
	if len(m.entries) < 2 {
		return
	}
	after := m.entries[0].ord
	page, err := m.store.GetTranscriptPage(m.ctx, m.session, api.TranscriptPageQuery{Limit: 2, After: &after})
	failErr(t, "page after the first message", err)
	ids := make([]string, len(page.Messages))
	for i, msg := range page.Messages {
		ids[i] = msg.ID
	}
	want := make([]string, 0, 2)
	for _, entry := range m.entries[1:min(3, len(m.entries))] {
		want = append(want, entry.id)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("page after ord %d = %v, want %v", after, ids, want)
	}
}

func TestTranscriptOrdIsMintedOnceAndNeverReused(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "ord-machine.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sqlStore := store.NewSQL(sqlDB)
	rapid.Check(t, func(t *rapid.T) {
		sess, err := sqlStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(t, "create session", err)
		machine := &ordMachine{ctx: ctx, store: sqlStore, session: sess.ID, seq: map[string]int64{}}
		t.Repeat(rapid.StateMachineActions(machine))
	})
}
