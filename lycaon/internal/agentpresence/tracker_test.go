package agentpresence

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

const project = "11111111-1111-1111-1111-111111111111"

type fakeChats map[string]ChatRef

func (f fakeChats) Chat(_ context.Context, sessionID string) (ChatRef, bool) {
	ref, ok := f[sessionID]
	return ref, ok
}

type fakeAnchors struct {
	mu       sync.Mutex
	epoch    int64
	revision int64
	paths    map[string]string
	hold     func(spans []AnchoredSpan) []bool
}

func (f *fakeAnchors) AnchorSpans(_ context.Context, _, documentID string, revision int64, spans []Span) (Anchored, error) {
	out := Anchored{DocumentID: documentID, Epoch: f.epoch, Revision: revision}
	if revision == 0 {
		out.Revision = f.revision
	}
	for i, span := range spans {
		out.Spans = append(out.Spans, AnchoredSpan{Anchor: []byte(fmt.Sprintf("a%d:%d", i, span.StartLine)), Head: []byte(fmt.Sprintf("h%d:%d", i, span.EndLine)), Expected: "text", Checkable: true})
	}
	return out, nil
}

func (f *fakeAnchors) AnchorPathSpans(ctx context.Context, projectID string, target Target, spans []Span) (Anchored, bool, error) {
	id, ok := f.paths[target.RootID+"/"+target.Path]
	if !ok {
		return Anchored{}, false, nil
	}
	out, err := f.AnchorSpans(ctx, projectID, id, 0, spans)
	return out, true, err
}

func (f *fakeAnchors) SpansHold(_ context.Context, _, _ string, spans []AnchoredSpan) ([]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hold(spans), nil
}

type recorder struct {
	mu      sync.Mutex
	events  []api.AgentPresenceEvent
	tracker *Tracker
}

func (r *recorder) PublishAgentPresence(_ context.Context, ev api.AgentPresenceEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) last(t *testing.T) api.AgentPresenceEvent {
	t.Helper()
	r.tracker.Settle()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		t.Fatal("nothing published")
	}
	return r.events[len(r.events)-1]
}

func newFixture() (*Tracker, *fakeAnchors, *recorder) {
	chats := fakeChats{
		"chat":   {ProjectID: project, SessionID: "chat", Title: "Config defaults"},
		"worker": {ProjectID: project, SessionID: "chat", JobID: "job-1"},
		"other":  {ProjectID: project, SessionID: "other", Title: "Scan cleanup"},
	}
	anchors := &fakeAnchors{epoch: 3, revision: 9, paths: map[string]string{"root/a.go": "doc-a"}, hold: func(spans []AnchoredSpan) []bool {
		return make([]bool, len(spans))
	}}
	rec := &recorder{}
	tracker := New(chats, rec)
	tracker.SetAnchors(anchors)
	rec.tracker = tracker
	return tracker, anchors, rec
}

func line(start, end int) Span { return Span{StartLine: start, EndLine: end} }

func TestWorkerItemsFoldIntoTheirChat(t *testing.T) {
	tracker, _, rec := newFixture()
	ctx := t.Context()
	tracker.ObserveSession(ctx, api.SessionEvent{ID: "chat", Status: api.SessionStatusBusy, CurrentTurn: 2})
	tracker.CallStarted(ctx, Call{SessionID: "worker", ToolCallID: "c1", Tool: "read"}, Target{RootID: "root", Path: "a.go"}, api.AgentActivityKindReading)
	tracker.ReadsReturned(ctx, Call{SessionID: "worker", ToolCallID: "c1", Tool: "read"}, []Read{{Target: Target{RootID: "root", Path: "a.go"}, Document: Document{ID: "doc-a", Revision: 4}, Extent: api.AgentPresenceExtentRange, Spans: []Span{line(3, 9)}}})
	ev := rec.last(t)
	if ev.SessionID != "chat" || ev.Presence.Title != "Config defaults" || ev.Presence.Turn != 2 {
		t.Fatalf("worker read published under %q title %q turn %d", ev.SessionID, ev.Presence.Title, ev.Presence.Turn)
	}
	read := ev.Presence.Reads[0]
	if read.WorkerID != "job-1" || read.DocumentID != "doc-a" || read.Epoch != 3 || read.Revision != 4 || string(read.Ranges[0].Anchor) != "a0:3" || read.Sequence != 1 {
		t.Fatalf("read = %+v", read)
	}
	if len(ev.Presence.Activities) != 1 || ev.Presence.Activities[0].WorkerID != "job-1" {
		t.Fatalf("activities = %+v", ev.Presence.Activities)
	}
}

func TestTurnEndClearsTheTurnsReads(t *testing.T) {
	tracker, _, rec := newFixture()
	ctx := t.Context()
	tracker.ObserveSession(ctx, api.SessionEvent{ID: "chat", Status: api.SessionStatusBusy, CurrentTurn: 1})
	tracker.ReadsReturned(ctx, Call{SessionID: "chat", ToolCallID: "c1", Tool: "read"}, []Read{{Target: Target{RootID: "root", Path: "a.go"}, Extent: api.AgentPresenceExtentWholeFile}})
	if got := snapshot(tracker).Sessions; len(got) != 1 || len(got[0].Reads) != 1 {
		t.Fatalf("read during the turn = %+v", got)
	}
	tracker.ObserveSession(ctx, api.SessionEvent{ID: "chat", Status: api.SessionStatusIdle, IdleDisposition: api.SessionIdleDispositionCompleted})
	if ev := rec.last(t); len(ev.Presence.Reads) != 0 || len(ev.Presence.Activities) != 0 {
		t.Fatalf("after turn end = %+v", ev.Presence)
	}
	if len(snapshot(tracker).Sessions) != 0 {
		t.Fatal("an idle chat without presence stayed in the snapshot")
	}
}

func TestIntentsFollowApprovalAndLanding(t *testing.T) {
	tracker, _, rec := newFixture()
	ctx := t.Context()
	call := Call{SessionID: "chat", ToolCallID: "w1", Tool: "replace_lines"}
	tracker.IntentsResolved(ctx, call, []Intent{{Target: Target{RootID: "root", Path: "a.go"}, Operation: api.AgentIntentOperationEdit, Document: Document{ID: "doc-a", Revision: 9}, Extent: api.AgentPresenceExtentRange, Spans: []Span{line(16, 18)}}})
	tracker.IntentsAwaitingApproval(ctx, call, "cp-1")
	intent := rec.last(t).Presence.Intents[0]
	if intent.State != api.AgentIntentStateAwaitingApproval || intent.CheckpointID != "cp-1" || intent.DocumentID != "doc-a" {
		t.Fatalf("held intent = %+v", intent)
	}
	tracker.IntentsApproved(ctx, call)
	if got := rec.last(t).Presence.Intents[0]; got.State != api.AgentIntentStatePending || got.CheckpointID != "" {
		t.Fatalf("approved intent = %+v", got)
	}
	tracker.IntentsLanded(ctx, call, []Document{{ID: "doc-a", Revision: 10}})
	if got := rec.last(t).Presence; len(got.Intents) != 0 {
		t.Fatalf("landed intents remain: %+v", got.Intents)
	}
}

func TestStaleReadsIgnoreTheChatsOwnLanding(t *testing.T) {
	tracker, anchors, rec := newFixture()
	ctx := t.Context()
	read := Read{Target: Target{RootID: "root", Path: "a.go"}, Document: Document{ID: "doc-a", Revision: 4}, Extent: api.AgentPresenceExtentRange, Spans: []Span{line(1, 2)}}
	tracker.ReadsReturned(ctx, Call{SessionID: "chat", ToolCallID: "r1", Tool: "read"}, []Read{read})
	tracker.ReadsReturned(ctx, Call{SessionID: "other", ToolCallID: "r2", Tool: "read"}, []Read{read})
	tracker.IntentsLanded(ctx, Call{SessionID: "chat", ToolCallID: "w1"}, []Document{{ID: "doc-a", Revision: 5}})
	tracker.DocumentChanged(ctx, project, "doc-a", 5)
	byChat := map[string]api.AgentSessionPresence{}
	for _, s := range snapshot(tracker).Sessions {
		byChat[s.SessionID] = s
	}
	if byChat["chat"].Reads[0].Stale || !byChat["other"].Reads[0].Stale {
		t.Fatalf("stale: chat=%v other=%v", byChat["chat"].Reads[0].Stale, byChat["other"].Reads[0].Stale)
	}
	anchors.mu.Lock()
	anchors.hold = func(spans []AnchoredSpan) []bool {
		out := make([]bool, len(spans))
		for i := range out {
			out[i] = true
		}
		return out
	}
	anchors.mu.Unlock()
	tracker.Settle()
	before := len(rec.events)
	tracker.DocumentChanged(ctx, project, "doc-a", 6)
	tracker.Settle()
	if len(rec.events) != before {
		t.Fatal("holding spans republished")
	}
}

type fakeDrafts map[string][]DraftFile

func (f fakeDrafts) ReadyFiles(_ context.Context, jobID string) ([]DraftFile, error) {
	return f[jobID], nil
}

func deliver(t *testing.T, tracker *Tracker, topic api.EventTopic, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", topic, err)
	}
	if err := tracker.ObserveDelivered(t.Context(), topic, data); err != nil {
		t.Fatalf("observe %s: %v", topic, err)
	}
}

func TestWorkerDraftsProgressAndSettle(t *testing.T) {
	tracker, _, rec := newFixture()
	ctx := t.Context()
	a := Target{RootID: "root", Path: "a.go"}
	insertions, deletions := 4, 3
	tracker.SetDrafts(fakeDrafts{"job-1": {{Target: a, Insertions: &insertions, Deletions: &deletions, Spans: []Span{line(8, 10)}}}})
	state := func() api.AgentWorkerDraftState {
		t.Helper()
		drafts := rec.last(t).Presence.WorkerDrafts
		if len(drafts) != 1 {
			t.Fatalf("drafts = %+v", drafts)
		}
		return drafts[0].State
	}

	tracker.WorkerReserved(ctx, "worker", []Target{a})
	if got := state(); got != api.AgentWorkerDraftStateReserved {
		t.Fatalf("after reservation state = %s", got)
	}
	deliver(t, tracker, api.EventTopicSourceChanged, api.SourceChangesEvent{ProjectID: project, WorkspaceKind: api.SourceWorkspaceKindWorker,
		Changes: []api.SourceChange{{RootID: "root", Path: "a.go", Op: api.SourceChangeOpWrite, SessionID: "worker", WorkerID: "job-1"}}})
	if got := state(); got != api.AgentWorkerDraftStateDrafting {
		t.Fatalf("after a worker write state = %s", got)
	}
	rec.tracker.Settle()
	rec.mu.Lock()
	beforeReady := len(rec.events)
	rec.mu.Unlock()
	deliver(t, tracker, api.EventTopicWorker, api.WorkerEvent{WorkerID: "job-1", ParentSessionID: "chat", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending})
	draft := rec.last(t).Presence.WorkerDrafts
	rec.mu.Lock()
	// Drafting becomes ready in one change; the file never shows no draft in between.
	if published := rec.events[beforeReady:]; len(published) != 1 {
		t.Fatalf("ready published %d events, want one", len(published))
	}
	rec.mu.Unlock()
	if len(draft) != 1 || draft[0].State != api.AgentWorkerDraftStateReady || draft[0].Extent != api.AgentPresenceExtentRange || *draft[0].Insertions != 4 || draft[0].DocumentID != "doc-a" {
		t.Fatalf("ready draft = %+v", draft)
	}
	deliver(t, tracker, api.EventTopicWorker, api.WorkerEvent{WorkerID: "job-1", ParentSessionID: "chat", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusApplying})
	if got := state(); got != api.AgentWorkerDraftStateLanding {
		t.Fatalf("while applying state = %s", got)
	}
	deliver(t, tracker, api.EventTopicWorker, api.WorkerEvent{WorkerID: "job-1", ParentSessionID: "chat", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusMerged})
	tracker.Settle()
	if len(snapshot(tracker).Sessions) != 0 {
		t.Fatal("merged job left presence")
	}
}

// A promotion that was pending when the host stopped still shows its drafts.
func TestRestoredReadyDraftsMatchALiveCompletion(t *testing.T) {
	a := Target{RootID: "root", Path: "a.go"}
	insertions, deletions := 2, 1
	drafts := fakeDrafts{"job-1": {{Target: a, Insertions: &insertions, Deletions: &deletions, Spans: []Span{line(3, 4)}}}}

	live, _, liveRec := newFixture()
	live.SetDrafts(drafts)
	deliver(t, live, api.EventTopicWorker, api.WorkerEvent{WorkerID: "job-1", ParentSessionID: "chat", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusPending})

	restored, _, restoredRec := newFixture()
	restored.SetDrafts(drafts)
	restored.RestoreReadyDrafts(t.Context(), []ReadyJob{{ChatSessionID: "chat", JobID: "job-1"}, {ChatSessionID: "gone", JobID: "job-2"}})

	want, got := liveRec.last(t).Presence.WorkerDrafts, restoredRec.last(t).Presence.WorkerDrafts
	if len(got) != 1 || got[0].State != api.AgentWorkerDraftStateReady || got[0].WorkerID != "job-1" {
		t.Fatalf("restored drafts = %+v", got)
	}
	if got[0].Path != want[0].Path || *got[0].Insertions != *want[0].Insertions || got[0].DocumentID != want[0].DocumentID {
		t.Fatalf("restored draft %+v differs from live %+v", got[0], want[0])
	}
	deliver(t, restored, api.EventTopicWorker, api.WorkerEvent{WorkerID: "job-1", ParentSessionID: "chat", Status: api.WorkerStatusComplete, MergeStatus: api.WorkerMergeStatusMerged})
	restored.Settle()
	if len(snapshot(restored).Sessions) != 0 {
		t.Fatal("a restored draft outlived its promotion")
	}
}

func TestIdleChatDropsItemsOfAWorkerThatEnds(t *testing.T) {
	tracker, _, _ := newFixture()
	ctx := t.Context()
	tracker.ReadsReturned(ctx, Call{SessionID: "worker", ToolCallID: "c1", Tool: "read"}, []Read{{Target: Target{RootID: "root", Path: "a.go"}, Extent: api.AgentPresenceExtentWholeFile}})
	tracker.ObserveSession(ctx, api.SessionEvent{ID: "chat", Status: api.SessionStatusIdle, IdleDisposition: api.SessionIdleDispositionCompleted})
	tracker.ReadsReturned(ctx, Call{SessionID: "worker", ToolCallID: "c2", Tool: "read"}, []Read{{Target: Target{RootID: "root", Path: "a.go"}, Extent: api.AgentPresenceExtentWholeFile}})
	if len(snapshot(tracker).Sessions[0].Reads) != 1 {
		t.Fatal("background worker read missing")
	}
	tracker.ObserveSession(ctx, api.SessionEvent{ID: "worker", Status: api.SessionStatusIdle, IdleDisposition: api.SessionIdleDispositionCompleted})
	if len(snapshot(tracker).Sessions) != 0 {
		t.Fatal("ended worker left reads on an idle chat")
	}
}

func TestReadsStayWithinTheirBudgets(t *testing.T) {
	tracker, _, _ := newFixture()
	ctx := t.Context()
	spans := make([]Span, maxItemRanges+10)
	for i := range spans {
		spans[i] = line(i+1, i+1)
	}
	for i := range 6 {
		tracker.ReadsReturned(ctx, Call{SessionID: "chat", ToolCallID: fmt.Sprint(i), Tool: "grep"}, []Read{{Target: Target{RootID: "root", Path: "a.go"}, Extent: api.AgentPresenceExtentMatches, Spans: spans}})
	}
	reads := snapshot(tracker).Sessions[0].Reads
	total := 0
	for _, r := range reads {
		if len(r.Ranges) > maxItemRanges {
			t.Fatalf("read kept %d ranges", len(r.Ranges))
		}
		total += len(r.Ranges)
	}
	if total > maxChatRanges || reads[len(reads)-1].ToolCallID != "5" {
		t.Fatalf("kept %d ranges, newest %q", total, reads[len(reads)-1].ToolCallID)
	}
}

func TestUnknownSessionsAndTargetsAreIgnored(t *testing.T) {
	tracker, _, rec := newFixture()
	ctx := t.Context()
	tracker.CallStarted(ctx, Call{SessionID: "missing", ToolCallID: "c"}, Target{RootID: "root", Path: "a.go"}, api.AgentActivityKindReading)
	tracker.CallStarted(ctx, Call{SessionID: "chat", ToolCallID: "c"}, Target{RootID: "", Path: "a.go"}, api.AgentActivityKindReading)
	if tracker.Settle(); len(rec.events) != 0 || len(snapshot(tracker).Sessions) != 0 {
		t.Fatal("invalid presence recorded")
	}
}

func snapshot(tracker *Tracker) api.AgentPresenceSnapshot {
	tracker.Settle()
	return tracker.Snapshot(project)
}

func TestChatTitlesFollowRenamesWithOrWithoutPresence(t *testing.T) {
	tracker, _, rec := newFixture()
	ctx := t.Context()
	read := []Read{{Target: Target{RootID: "root", Path: "a.go"}, Extent: api.AgentPresenceExtentWholeFile}}
	// Session lookups are cached without titles, so titles come only from rename facts.
	tracker.chats = fakeChats{"chat": {ProjectID: project, SessionID: "chat"}}
	tracker.ObserveSession(ctx, api.SessionEvent{ID: "chat", Title: "Named before any presence"})
	tracker.ReadsReturned(ctx, Call{SessionID: "chat", ToolCallID: "c1", Tool: "read"}, read)
	if got := rec.last(t).Presence.Title; got != "Named before any presence" {
		t.Fatalf("title after first read = %q", got)
	}
	tracker.ObserveSession(ctx, api.SessionEvent{ID: "chat", Title: "Renamed"})
	if got := rec.last(t).Presence.Title; got != "Renamed" {
		t.Fatalf("title after rename = %q", got)
	}
}
