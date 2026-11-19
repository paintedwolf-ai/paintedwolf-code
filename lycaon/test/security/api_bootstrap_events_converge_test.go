package security

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
	"pgregory.net/rapid"
)

// resumeMode is how the late subscriber lives through the events after its
// bootstrap.
type resumeMode int

const (
	stayConnected resumeMode = iota
	// resumeAfter drops the stream and reconnects with ?after= the last
	// cursor it delivered.
	resumeAfter
)

// TestBootstrapPlusEventsConverges: a chat bootstrapped at cursor k plus the
// events after k equals a fresh bootstrap and the whole stored transcript,
// for a subscriber that stays and one that resumes; a subscriber that watched
// from the start agrees with both. A cursor that left the replay window, and a
// stream resumed at a bootstrap taken right after a write, are fixed cases.
func TestBootstrapPlusEventsConverges(t *testing.T) {
	// Every generated case creates two chats; admission limits are their own contract.
	t.Setenv("LYCAON_RATE_SESSIONS_PER_MIN", "100000000")
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	srv := httptest.NewServer(h.Server)
	t.Cleanup(srv.Close)
	dir := h.ProjectDir(t, "converge")
	project := CreateTestProjectHTTP(t, h.Server, dir)
	checkpoints, ok := h.Sessions.Checkpoints.(*hitl.Checkpoints)
	if !ok {
		t.Fatalf("checkpoint manager is %T, want *hitl.Checkpoints", h.Sessions.Checkpoints)
	}
	world := &convergeWorld{t: t, h: h, base: srv.URL, dir: dir, project: project, checkpoints: checkpoints}

	rapid.Check(t, func(rt *rapid.T) {
		world.settle()
		early := world.subscribe(world.currentCursor())
		defer early.close()
		chats := [2]string{
			CreateSessionForProjectHTTP(t, h.Server, project.ID, wire.SessionPostureBuild).ID,
			CreateSessionForProjectHTTP(t, h.Server, project.ID, wire.SessionPostureBuild).ID,
		}
		run := &convergeRun{world: world, chats: chats}
		steps := rapid.IntRange(1, 24).Draw(rt, "steps")
		cut := rapid.IntRange(0, steps).Draw(rt, "cut")
		mode := rapid.SampledFrom([]resumeMode{stayConnected, resumeAfter}).Draw(rt, "mode")
		for range cut {
			run.step(rt)
		}
		world.settle()
		late := map[string]convergeState{}
		// A client holding two bootstraps resumes from the earlier cursor.
		var cursor string
		for _, chat := range chats {
			boot := world.bootstrap(chat)
			late[chat] = stateFromBootstrap(boot)
			if cursor == "" {
				cursor = boot.EventCursor
			}
		}
		stream := world.subscribe(cursor)
		var delivered []wire.EventEnvelope
		for i := cut; i < steps; i++ {
			if i == (cut+steps)/2 && mode == resumeAfter {
				delivered = append(delivered, world.sync(stream)...)
				stream.close()
				stream = world.subscribe(lastCursor(delivered, cursor))
			}
			run.step(rt)
		}
		world.settle()
		delivered = append(delivered, world.sync(stream)...)
		stream.close()
		world.sync(early)
		watched := early.synced

		requireIncreasingCursors(rt, "late subscriber", delivered)
		requireIncreasingCursors(rt, "early subscriber", watched)
		for _, chat := range chats {
			fresh := stateFromBootstrap(world.bootstrap(chat))
			fresh.messages = messageRevisions(pageWholeTranscript(t, h.Server, chat))
			fromLate := late[chat].apply(chat, delivered)
			fromStart := convergeState{messages: map[string]int64{}, checkpoints: map[string]bool{}, workers: map[string]wire.WorkerStatus{}}.apply(chat, watched)
			for name, folded := range map[string]convergeState{"bootstrap at k plus events": fromLate, "every event from the start": fromStart} {
				if diff := fresh.diff(folded); diff != "" {
					rt.Fatalf("chat %s, %s (mode %d, cut %d of %d): %s", chat, name, mode, cut, steps, diff)
				}
			}
			for id := range run.appended[chat] {
				if _, ok := fresh.messages[id]; !ok {
					rt.Fatalf("chat %s lost appended message %s", chat, id)
				}
			}
		}
		if pending := world.outboxRows(); pending != 0 {
			rt.Fatalf("%d outbox rows are still undelivered after settling", pending)
		}
	})

	t.Run("an expired cursor continues from a fresh bootstrap", func(t *testing.T) {
		world := world.on(t)
		world.settle()
		watching := world.subscribe(world.currentCursor())
		defer watching.close()
		chat := CreateSessionForProjectHTTP(t, h.Server, project.ID, wire.SessionPostureBuild).ID
		world.append(chat)
		world.settle()
		stream := world.subscribe(world.bootstrap(chat).EventCursor)
		world.append(chat)
		world.settle()
		delivered := world.sync(stream)
		stream.close()
		world.expireReplay(watching)
		world.resubscribeExpired(lastCursor(delivered, ""))
		boot := world.bootstrap(chat)
		stream = world.subscribe(boot.EventCursor)
		defer stream.close()
		world.append(chat)
		world.settle()
		world.requireConverged(chat, stateFromBootstrap(boot).apply(chat, world.sync(stream)))
	})

	t.Run("a stream resumed at the bootstrap's cursor delivers only later events", func(t *testing.T) {
		world := world.on(t)
		chat := CreateSessionForProjectHTTP(t, h.Server, project.ID, wire.SessionPostureBuild).ID
		atCut := world.append(chat)
		world.settle()
		boot := world.bootstrap(chat)
		if _, ok := stateFromBootstrap(boot).messages[atCut]; !ok {
			t.Fatalf("the bootstrap taken after message %s does not hold it", atCut)
		}
		stream := world.subscribe(boot.EventCursor)
		defer stream.close()
		after := world.append(chat)
		world.settle()
		delivered := world.sync(stream)
		cut, ok := cursorSequence(boot.EventCursor)
		if !ok {
			t.Fatalf("the bootstrap names an unreadable cursor %q", boot.EventCursor)
		}
		for _, env := range delivered {
			if sequence, _ := cursorSequence(env.Cursor); sequence <= cut {
				t.Fatalf("%s event at cursor %d, inside the bootstrap at %d, was delivered again", env.Topic, sequence, cut)
			}
		}
		folded := stateFromBootstrap(boot).apply(chat, delivered)
		if _, ok := folded.messages[after]; !ok {
			t.Fatalf("message %s, written after the cursor, was not delivered", after)
		}
		world.requireConverged(chat, folded)
	})
}

// on is the same world failing through t, for a subtest.
func (w *convergeWorld) on(t *testing.T) *convergeWorld {
	scoped := *w
	scoped.t = t
	return &scoped
}

// append adds an assistant message to chat through the store's outbox and
// answers its id.
func (w *convergeWorld) append(chat string) string {
	msg := wire.Message{ID: uuid.NewString(), Role: wire.MessageRoleAssistant, Origin: wire.MessageOriginModel, Content: "appended " + uuid.NewString()}
	testutil.FailErr(w.t, "append message", w.h.Sessions.Store.AppendMessages(w.t.Context(), chat, msg))
	return msg.ID
}

// requireConverged compares a folded state with a fresh bootstrap and the
// whole stored transcript.
func (w *convergeWorld) requireConverged(chat string, folded convergeState) {
	w.t.Helper()
	fresh := stateFromBootstrap(w.bootstrap(chat))
	fresh.messages = messageRevisions(pageWholeTranscript(w.t, w.h.Server, chat))
	if diff := fresh.diff(folded); diff != "" {
		w.t.Fatalf("chat %s: %s", chat, diff)
	}
}

// convergeWorld is one host, one project, and the HTTP client the subscribers use.
type convergeWorld struct {
	t           *testing.T
	h           *wiring.Harness
	base        string
	dir         string
	project     wire.Project
	checkpoints *hitl.Checkpoints
}

// convergeRun applies generated mutations to two chats and remembers what
// each chat holds, so later steps can patch and resolve it.
type convergeRun struct {
	world    *convergeWorld
	chats    [2]string
	appended map[string]map[string]bool
	messages map[string][]string
	pending  map[string][]string
	workers  map[string][]string
}

func (r *convergeRun) step(rt *rapid.T) {
	if r.appended == nil {
		r.appended, r.messages = map[string]map[string]bool{}, map[string][]string{}
		r.pending, r.workers = map[string][]string{}, map[string][]string{}
	}
	t, ctx := r.world.t, r.world.t.Context()
	chat := r.chats[rapid.IntRange(0, 1).Draw(rt, "chat")]
	switch kind := rapid.SampledFrom([]string{"append", "patch", "checkpoint", "resolve", "worker", "cancel"}).Draw(rt, "op"); {
	case kind == "patch" && len(r.messages[chat]) > 0:
		id := rapid.SampledFrom(r.messages[chat]).Draw(rt, "patched")
		msg := wire.Message{ID: id, Role: wire.MessageRoleAssistant, Origin: wire.MessageOriginModel, Content: "patched " + uuid.NewString()}
		_, err := r.world.h.Sessions.Store.UpdateMessage(ctx, chat, id, msg)
		testutil.FailErr(t, "patch message", err)
	case kind == "checkpoint":
		before := "one\n"
		resp, err := r.world.checkpoints.RequestCheckpoint(ctx, hitl.CheckpointRequest{
			SessionID: chat, Kind: wire.CheckpointKindContentApply,
			ContentApply: &hitl.ContentApplyPayload{Tool: "edit", ToolCallID: uuid.NewString(), Path: "notes.txt", Before: &before, After: "two\n"},
		})
		testutil.FailErr(t, "open checkpoint", err)
		r.pending[chat] = append(r.pending[chat], resp.CheckpointID)
	case kind == "resolve" && len(r.pending[chat]) > 0:
		id := r.pending[chat][0]
		r.pending[chat] = r.pending[chat][1:]
		_, err := r.world.checkpoints.ResolveCheckpoint(r.world.h.OwnerCtx(t, ctx), chat, id, wire.CheckpointKindContentApply, nil,
			&hitl.ContentApplyResolve{Decision: wire.ContentApplyReject})
		testutil.FailErr(t, "resolve checkpoint", err)
	case kind == "worker":
		id, err := r.world.h.Workers.Queue.Enqueue(ctx, wire.WorkerTask{
			Prompt: "converge", Brief: "converge", ParentSessionID: chat, ProjectID: r.world.project.ID,
			WorkspaceRootID: r.world.project.Roots[0].ID, WorkspacePath: r.world.dir, AgentType: "implementer",
			Scope: &wire.TaskScope{Mode: wire.TaskScopeModeRead},
		})
		testutil.FailErr(t, "enqueue worker", err)
		r.workers[chat] = append(r.workers[chat], id)
	case kind == "cancel" && len(r.workers[chat]) > 0:
		id := r.workers[chat][0]
		r.workers[chat] = r.workers[chat][1:]
		testutil.FailErr(t, "cancel worker", r.world.h.Workers.Queue.Cancel(ctx, id, nil))
	default:
		msg := wire.Message{ID: uuid.NewString(), Role: wire.MessageRoleAssistant, Origin: wire.MessageOriginModel, Content: "appended " + uuid.NewString()}
		testutil.FailErr(t, "append message", r.world.h.Sessions.Store.AppendMessages(ctx, chat, msg))
		if r.appended[chat] == nil {
			r.appended[chat] = map[string]bool{}
		}
		r.appended[chat][msg.ID] = true
		r.messages[chat] = append(r.messages[chat], msg.ID)
	}
}

// convergeState is what a client holds for one chat: each message's highest
// transcript seq, the pending checkpoints, and each worker's status.
type convergeState struct {
	messages    map[string]int64
	checkpoints map[string]bool
	workers     map[string]wire.WorkerStatus
}

func stateFromBootstrap(boot wire.SessionBootstrap) convergeState {
	state := convergeState{messages: messageRevisions(boot.Transcript.Messages), checkpoints: map[string]bool{}, workers: map[string]wire.WorkerStatus{}}
	for _, cp := range boot.Checkpoints {
		state.checkpoints[cp.ID] = true
	}
	for _, worker := range boot.Workers {
		state.workers[worker.ID] = worker.Status
	}
	return state
}

func messageRevisions(messages []wire.Message) map[string]int64 {
	out := make(map[string]int64, len(messages))
	for _, msg := range messages {
		out[msg.ID] = msg.Seq
	}
	return out
}

// apply folds chat's events into a copy of the state, as Den does: a row is
// replaced only by a newer seq.
func (s convergeState) apply(chat string, envelopes []wire.EventEnvelope) convergeState {
	out := convergeState{messages: maps.Clone(s.messages), checkpoints: maps.Clone(s.checkpoints), workers: maps.Clone(s.workers)}
	for _, env := range envelopes {
		switch env.Topic {
		case wire.EventTopicMessage:
			var ev wire.MessageEvent
			if json.Unmarshal(env.Data, &ev) == nil && ev.SessionID == chat && ev.Message.Seq > out.messages[ev.Message.ID] {
				out.messages[ev.Message.ID] = ev.Message.Seq
			}
		case wire.EventTopicCheckpoint:
			var ev wire.CheckpointEvent
			if json.Unmarshal(env.Data, &ev) != nil || ev.SessionID != chat {
				continue
			}
			if ev.Status == wire.CheckpointStatusPending {
				out.checkpoints[ev.ID] = true
			} else {
				delete(out.checkpoints, ev.ID)
			}
		case wire.EventTopicWorker:
			var ev wire.WorkerEvent
			if json.Unmarshal(env.Data, &ev) == nil && ev.ParentSessionID == chat {
				out.workers[ev.WorkerID] = ev.Status
			}
		}
	}
	return out
}

// diff names the first entity whose state differs from fresh.
func (s convergeState) diff(folded convergeState) string {
	for _, id := range slices.Sorted(maps.Keys(s.messages)) {
		if folded.messages[id] != s.messages[id] {
			return fmt.Sprintf("message %s: folded seq %d, stored seq %d", id, folded.messages[id], s.messages[id])
		}
	}
	for id := range folded.messages {
		if _, ok := s.messages[id]; !ok {
			return fmt.Sprintf("message %s is folded but not stored", id)
		}
	}
	if !maps.Equal(s.checkpoints, folded.checkpoints) {
		return fmt.Sprintf("pending checkpoints: folded %v, fresh %v", slices.Sorted(maps.Keys(folded.checkpoints)), slices.Sorted(maps.Keys(s.checkpoints)))
	}
	if !maps.Equal(s.workers, folded.workers) {
		return fmt.Sprintf("workers: folded %v, fresh %v", folded.workers, s.workers)
	}
	return ""
}

// settle waits until every committed event row reached the hub and every
// debounced event was delivered.
func (w *convergeWorld) settle() {
	w.h.Server.WaitForBackground(w.t.Context())
	testutil.WaitFor(w.t, "outbox drained", func() bool { return w.outboxRows() == 0 })
	w.h.MemoryHub().FlushDebounced()
}

func (w *convergeWorld) outboxRows() int {
	var n int
	testutil.FailErr(w.t, "count outbox rows", w.h.DB.QueryRowContext(w.t.Context(), "SELECT COUNT(*) FROM event_outbox").Scan(&n))
	return n
}

func (w *convergeWorld) currentCursor() string {
	return w.h.MemoryHub().CurrentCursor()
}

func (w *convergeWorld) bootstrap(chat string) wire.SessionBootstrap {
	req := AuthedRequest(w.t, http.MethodGet, "/v1/sessions/"+chat+"/bootstrap", nil)
	rec := httptest.NewRecorder()
	w.h.Server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		w.t.Fatalf("bootstrap %s: %d %s", chat, rec.Code, rec.Body.String())
	}
	var boot wire.SessionBootstrap
	testutil.FailErr(w.t, "decode bootstrap", json.Unmarshal(rec.Body.Bytes(), &boot))
	return boot
}

// expireReplay publishes past the hub's replay window, in batches the
// watching stream drains so the hub never drops it.
func (w *convergeWorld) expireReplay(watching *convergeStream) {
	hub := w.h.MemoryHub()
	for published := 0; published <= events.ReplayCapacity; {
		for range events.SubscriberBufferSize / 2 {
			testutil.FailErr(w.t, "publish filler", hub.Publish(w.t.Context(), wire.EventTopicPreflight, events.PublishKey{}, wire.PreflightEvent{ProbeID: "converge-filler"}))
			published++
		}
		w.sync(watching)
	}
}

// resubscribeExpired requires the stream to refuse an expired cursor and name
// the cursor it would continue from; the client bootstraps again instead.
func (w *convergeWorld) resubscribeExpired(expired string) {
	resp := w.openStream(w.t.Context(), expired)
	defer resp.Body.Close()
	var refusal struct {
		Code    wire.ApiErrorCode `json:"code"`
		Details struct {
			EventCursor string `json:"event_cursor"`
		} `json:"details"`
	}
	testutil.FailErr(w.t, "decode replay refusal", json.NewDecoder(resp.Body).Decode(&refusal))
	if resp.StatusCode != http.StatusConflict || refusal.Code != wire.ApiErrorCodeEventReplayUnavailable || refusal.Details.EventCursor == "" {
		w.t.Fatalf("expired cursor answered %d %s with cursor %q, want 409 %s naming one", resp.StatusCode, refusal.Code, refusal.Details.EventCursor, wire.ApiErrorCodeEventReplayUnavailable)
	}
}

func (w *convergeWorld) openStream(ctx context.Context, after string) *http.Response {
	query := url.Values{"project_id": {w.project.ID}}
	if after != "" {
		query.Set("after", after)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/v1/events?"+query.Encode(), nil)
	testutil.FailErr(w.t, "build stream request", err)
	req.Header.Set("Authorization", api.TestAuthHeader())
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(w.t, "open event stream", err)
	return resp
}

// convergeStream is one live SSE connection and every envelope it delivered.
type convergeStream struct {
	mu        sync.Mutex
	envelopes []wire.EventEnvelope
	// arrived is signalled after each append.
	arrived chan struct{}
	// read is how many envelopes sync has consumed; synced holds the ones
	// it returned.
	read   int
	synced []wire.EventEnvelope
	ended  chan struct{}
	close  func()
}

func (w *convergeWorld) subscribe(after string) *convergeStream {
	ctx, cancel := context.WithCancel(w.t.Context())
	resp := w.openStream(ctx, after)
	if resp.StatusCode != http.StatusOK {
		cancel()
		w.t.Fatalf("event stream after %q answered %d", after, resp.StatusCode)
	}
	stream := &convergeStream{arrived: make(chan struct{}, 1), ended: make(chan struct{})}
	go func() {
		defer close(stream.ended)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var env wire.EventEnvelope
			if json.Unmarshal([]byte(data), &env) != nil {
				continue
			}
			stream.mu.Lock()
			stream.envelopes = append(stream.envelopes, env)
			stream.mu.Unlock()
			select {
			case stream.arrived <- struct{}{}:
			default:
			}
		}
	}()
	stream.close = func() {
		cancel()
		_ = resp.Body.Close()
		<-stream.ended
	}
	return stream
}

// sync publishes a marker after everything already delivered to the hub and
// returns what the stream delivered since the last sync, markers and filler
// left out.
func (w *convergeWorld) sync(stream *convergeStream) []wire.EventEnvelope {
	marker := "converge-" + uuid.NewString()
	testutil.FailErr(w.t, "publish marker", w.h.MemoryHub().Publish(w.t.Context(), wire.EventTopicPreflight, events.PublishKey{}, wire.PreflightEvent{ProbeID: marker}))
	for {
		stream.mu.Lock()
		var out []wire.EventEnvelope
		for i := stream.read; i < len(stream.envelopes); i++ {
			env := stream.envelopes[i]
			if env.Topic != wire.EventTopicPreflight {
				out = append(out, env)
				continue
			}
			var ev wire.PreflightEvent
			if json.Unmarshal(env.Data, &ev) == nil && ev.ProbeID == marker {
				stream.read = i + 1
				stream.synced = append(stream.synced, out...)
				stream.mu.Unlock()
				return out
			}
		}
		stream.mu.Unlock()
		select {
		case <-stream.arrived:
		case <-stream.ended:
			w.t.Fatal("the event stream ended before the marker arrived")
		case <-w.t.Context().Done():
			w.t.Fatal("the marker never arrived")
		}
	}
}

func lastCursor(envelopes []wire.EventEnvelope, fallback string) string {
	if len(envelopes) == 0 {
		return fallback
	}
	return envelopes[len(envelopes)-1].Cursor
}

// requireIncreasingCursors reads each cursor's sequence; delivery order must
// follow it strictly.
func requireIncreasingCursors(rt *rapid.T, who string, envelopes []wire.EventEnvelope) {
	var previous uint64
	for i, env := range envelopes {
		sequence, ok := cursorSequence(env.Cursor)
		if !ok {
			rt.Fatalf("%s: event %d carries an unreadable cursor %q", who, i, env.Cursor)
		}
		if sequence <= previous {
			rt.Fatalf("%s: event %d cursor %d does not follow %d", who, i, sequence, previous)
		}
		previous = sequence
	}
}

// cursorSequence reads the hub sequence a cursor names.
func cursorSequence(cursor string) (uint64, bool) {
	payload, _, _ := strings.Cut(cursor, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	var decoded struct {
		Sequence uint64 `json:"s"`
	}
	if err != nil || json.Unmarshal(raw, &decoded) != nil {
		return 0, false
	}
	return decoded.Sequence, true
}
