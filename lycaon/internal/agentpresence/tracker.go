package agentpresence

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// Bounds keep one chat's presence small enough to publish whole on every change.
const (
	maxActivities = 64
	maxReads      = 256
	maxChatRanges = 2048
	maxItemRanges = 512
	maxIntents    = 64
	maxDrafts     = 256
)

// Tracker is the project-scoped agent presence projection.
type Tracker struct {
	anchors   Anchors
	chats     Chats
	publisher Publisher
	drafts    Drafts

	mu       sync.Mutex
	projects map[string]*projectPresence
	// titles holds each chat's newest title, including while it has no presence.
	titles map[string]string
	nextID uint64
	queue  workQueue
}

// maxTitles bounds remembered chat titles.
const maxTitles = 1024

type projectPresence struct {
	revision int64
	chats    map[string]*chatPresence
}

type chatPresence struct {
	sessionID string
	title     string
	turn      int
	running   bool
	sequence  int64

	activities []api.AgentActivity
	reads      []*anchoredRead
	intents    []api.AgentIntent
	drafts     []api.AgentWorkerDraft
	// landed holds the document revisions this chat's own edits produced, so
	// they never mark the chat's reads stale.
	landed map[string]int64
}

// anchoredRead keeps the spans a read was anchored with, for staleness checks.
type anchoredRead struct {
	wire     api.AgentRead
	document string
	revision int64
	spans    []AnchoredSpan
}

// New builds a tracker; with a nil publisher it keeps snapshots only.
func New(chats Chats, publisher Publisher) *Tracker {
	return &Tracker{chats: chats, publisher: publisher, projects: make(map[string]*projectPresence)}
}

// callStarted records a running call with one file target.
func (t *Tracker) callStarted(ctx context.Context, call Call, target Target, kind api.AgentActivityKind) {
	ref, ok := t.chat(ctx, call.SessionID)
	if !ok || !validTarget(target) {
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		c.running = true
		c.activities = slices.DeleteFunc(c.activities, func(a api.AgentActivity) bool { return a.ToolCallID == call.ToolCallID })
		c.activities = append(c.activities, api.AgentActivity{ToolCallID: call.ToolCallID, WorkerID: ref.JobID, Tool: call.Tool, RootID: target.RootID, Path: target.Path, Kind: kind})
		c.activities = keepNewest(c.activities, maxActivities)
		return true
	})
}

// callEnded removes the call's activity and any of its intents that did not land.
func (t *Tracker) callEnded(ctx context.Context, call Call) {
	ref, ok := t.chat(ctx, call.SessionID)
	if !ok {
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		before := len(c.activities) + len(c.intents)
		c.activities = slices.DeleteFunc(c.activities, func(a api.AgentActivity) bool { return a.ToolCallID == call.ToolCallID })
		c.intents = slices.DeleteFunc(c.intents, func(i api.AgentIntent) bool { return i.ToolCallID == call.ToolCallID })
		return len(c.activities)+len(c.intents) != before
	})
}

// readsReturned records text a call returned to the model.
func (t *Tracker) readsReturned(ctx context.Context, call Call, reads []Read) {
	ref, ok := t.chat(ctx, call.SessionID)
	if !ok || len(reads) == 0 {
		return
	}
	items := make([]*anchoredRead, 0, len(reads))
	for _, read := range reads {
		if !validTarget(read.Target) {
			continue
		}
		spans := boundedSpans(read.Spans, maxItemRanges)
		anchored, anchoredOK := t.anchorDocument(ctx, ref.ProjectID, read.Document, spans)
		// A search reads the person's current text, so its lines anchor in the
		// current document. A worker searches its own branch, which has none.
		if !anchoredOK && read.Document.ID == "" && read.Extent == api.AgentPresenceExtentMatches && ref.JobID == "" {
			anchored, anchoredOK = t.anchorPath(ctx, ref.ProjectID, read.Target, spans)
		}
		wire := api.AgentRead{ToolCallID: call.ToolCallID, WorkerID: ref.JobID, Tool: call.Tool, RootID: read.RootID, Path: read.Path,
			Extent: read.Extent, Ranges: wireRanges(spans, anchored, anchoredOK)}
		item := &anchoredRead{wire: wire}
		if anchoredOK {
			item.document, item.revision, item.spans = anchored.DocumentID, anchored.Revision, anchored.Spans
			item.wire.DocumentID, item.wire.Epoch, item.wire.Revision = anchored.DocumentID, anchored.Epoch, anchored.Revision
		}
		items = append(items, item)
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		for _, item := range items {
			c.sequence++
			item.wire.Sequence = c.sequence
			item.wire.ID = t.id("read")
			c.reads = append(c.reads, item)
		}
		c.reads = boundReads(c.reads)
		return len(items) > 0
	})
}

// intentsResolved records a call's resolved, unlanded mutations.
func (t *Tracker) intentsResolved(ctx context.Context, call Call, intents []Intent) {
	ref, ok := t.chat(ctx, call.SessionID)
	if !ok {
		return
	}
	wires := make([]api.AgentIntent, 0, len(intents))
	for _, intent := range intents {
		if !validTarget(intent.Target) {
			continue
		}
		spans := boundedSpans(intent.Spans, maxItemRanges)
		anchored, anchoredOK := t.anchorDocument(ctx, ref.ProjectID, intent.Document, spans)
		wire := api.AgentIntent{WorkerID: ref.JobID, ToolCallID: call.ToolCallID, Tool: call.Tool, Operation: intent.Operation,
			State: api.AgentIntentStatePending, RootID: intent.RootID, Path: intent.Path, ToPath: intent.ToPath,
			Extent: intent.Extent, Ranges: wireRanges(spans, anchored, anchoredOK)}
		if anchoredOK {
			wire.DocumentID, wire.Epoch = anchored.DocumentID, anchored.Epoch
		}
		wires = append(wires, wire)
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		c.running = true
		// A call reports each target as it resolves; a report replaces that target only.
		c.intents = slices.DeleteFunc(c.intents, func(i api.AgentIntent) bool {
			return i.ToolCallID == call.ToolCallID && slices.ContainsFunc(wires, func(w api.AgentIntent) bool { return w.RootID == i.RootID && w.Path == i.Path })
		})
		for _, wire := range wires {
			wire.ID = t.id("intent")
			c.intents = append(c.intents, wire)
		}
		c.intents = keepNewest(c.intents, maxIntents)
		return true
	})
}

// intentsAwaitingApproval marks a call's intents as held by a checkpoint.
func (t *Tracker) intentsAwaitingApproval(ctx context.Context, call Call, checkpointID string) {
	t.setIntentState(ctx, call, api.AgentIntentStateAwaitingApproval, checkpointID)
}

// intentsApproved returns a call's held intents to pending.
func (t *Tracker) intentsApproved(ctx context.Context, call Call) {
	t.setIntentState(ctx, call, api.AgentIntentStatePending, "")
}

func (t *Tracker) setIntentState(ctx context.Context, call Call, state api.AgentIntentState, checkpointID string) {
	ref, ok := t.chat(ctx, call.SessionID)
	if !ok {
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		changed := false
		for i := range c.intents {
			if c.intents[i].ToolCallID == call.ToolCallID && (c.intents[i].State != state || c.intents[i].CheckpointID != checkpointID) {
				c.intents[i].State, c.intents[i].CheckpointID = state, checkpointID
				changed = true
			}
		}
		return changed
	})
}

// intentsLanded removes a call's intents once its mutation committed. The
// landed document revisions are the chat's own and never make its reads stale.
func (t *Tracker) intentsLanded(ctx context.Context, call Call, documents []Document) {
	ref, ok := t.chat(ctx, call.SessionID)
	if !ok {
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		for _, d := range documents {
			if d.ID != "" && d.Revision > c.landed[d.ID] {
				if c.landed == nil {
					c.landed = make(map[string]int64)
				}
				c.landed[d.ID] = d.Revision
			}
		}
		before := len(c.intents)
		c.intents = slices.DeleteFunc(c.intents, func(i api.AgentIntent) bool { return i.ToolCallID == call.ToolCallID })
		return len(c.intents) != before
	})
}

// turnStarted begins a chat turn: nothing from an earlier turn survives.
// Worker sessions do not start chat turns.
func (t *Tracker) turnStarted(ctx context.Context, sessionID string, turn int) {
	ref, ok := t.chat(ctx, sessionID)
	if !ok || ref.JobID != "" {
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		if c.running && c.turn == turn {
			return false
		}
		c.running, c.turn = true, turn
		c.activities, c.reads, c.intents = nil, nil, nil
		c.landed = nil
		return true
	})
}

// turnEnded ends a chat turn: activities, reads, and intents end. A worker
// session ending while its chat is idle removes that job's items.
func (t *Tracker) turnEnded(ctx context.Context, sessionID string) {
	ref, ok := t.chat(ctx, sessionID)
	if !ok {
		return
	}
	if ref.JobID != "" {
		t.mutate(ctx, ref, func(c *chatPresence) bool {
			if c.running {
				return false
			}
			return c.dropJobItems(ref.JobID)
		})
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		c.running = false
		c.activities, c.reads, c.intents = nil, nil, nil
		c.landed = nil
		return true
	})
}

// workerDrafts sets the state of a job's files. Files already recorded for the
// job keep their place; spans replace earlier spans when given.
func (t *Tracker) workerDrafts(ctx context.Context, chatSessionID, jobID string, state api.AgentWorkerDraftState, files []DraftFile) {
	ref, ok := t.chat(ctx, chatSessionID)
	if !ok || strings.TrimSpace(jobID) == "" {
		return
	}
	wires := t.draftWires(ctx, ref, jobID, state, files)
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		for _, wire := range wires {
			index := slices.IndexFunc(c.drafts, func(d api.AgentWorkerDraft) bool {
				return d.WorkerID == jobID && d.RootID == wire.RootID && d.Path == wire.Path
			})
			if index < 0 {
				c.drafts = append(c.drafts, wire)
				continue
			}
			if len(wire.Ranges) == 0 {
				wire.Extent, wire.Ranges, wire.DocumentID, wire.Epoch = c.drafts[index].Extent, c.drafts[index].Ranges, c.drafts[index].DocumentID, c.drafts[index].Epoch
			}
			if wire.Insertions == nil && wire.Deletions == nil {
				wire.Insertions, wire.Deletions = c.drafts[index].Insertions, c.drafts[index].Deletions
			}
			c.drafts[index] = wire
		}
		c.drafts = keepNewest(c.drafts, maxDrafts)
		return len(wires) > 0
	})
}

// workerReady replaces a completed job's drafts with the files its promotion
// would land, in one change so the files never briefly show no draft.
func (t *Tracker) workerReady(ctx context.Context, chatSessionID, jobID string, files []DraftFile) {
	ref, ok := t.chat(ctx, chatSessionID)
	if !ok || strings.TrimSpace(jobID) == "" {
		return
	}
	wires := t.draftWires(ctx, ref, jobID, api.AgentWorkerDraftStateReady, files)
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		c.drafts = slices.DeleteFunc(c.drafts, func(d api.AgentWorkerDraft) bool { return d.WorkerID == jobID })
		c.drafts = keepNewest(append(c.drafts, wires...), maxDrafts)
		return true
	})
}

// draftWires anchors a job's changed lines in the documents that exist for its files.
func (t *Tracker) draftWires(ctx context.Context, ref ChatRef, jobID string, state api.AgentWorkerDraftState, files []DraftFile) []api.AgentWorkerDraft {
	wires := make([]api.AgentWorkerDraft, 0, len(files))
	for _, file := range files {
		if !validTarget(file.Target) {
			continue
		}
		spans := boundedSpans(file.Spans, maxItemRanges)
		wire := api.AgentWorkerDraft{WorkerID: jobID, State: state, RootID: file.RootID, Path: file.Path,
			Insertions: file.Insertions, Deletions: file.Deletions, Extent: api.AgentPresenceExtentWholeFile, Ranges: []api.AgentTextRange{}}
		if len(spans) > 0 {
			anchored, anchoredOK := t.anchorPath(ctx, ref.ProjectID, file.Target, spans)
			wire.Extent, wire.Ranges = api.AgentPresenceExtentRange, wireRanges(spans, anchored, anchoredOK)
			if anchoredOK {
				wire.DocumentID, wire.Epoch = anchored.DocumentID, anchored.Epoch
			}
		}
		wires = append(wires, wire)
	}
	return wires
}

// workerLanding marks every file of a job as landing.
func (t *Tracker) workerLanding(ctx context.Context, chatSessionID, jobID string) {
	ref, ok := t.chat(ctx, chatSessionID)
	if !ok {
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		changed := false
		for i := range c.drafts {
			if c.drafts[i].WorkerID == jobID && c.drafts[i].State != api.AgentWorkerDraftStateLanding {
				c.drafts[i].State = api.AgentWorkerDraftStateLanding
				changed = true
			}
		}
		return changed
	})
}

// workerSettled removes a job's drafts once it landed, was rejected, or ended.
func (t *Tracker) workerSettled(ctx context.Context, chatSessionID, jobID string) {
	ref, ok := t.chat(ctx, chatSessionID)
	if !ok {
		return
	}
	t.mutate(ctx, ref, func(c *chatPresence) bool {
		before := len(c.drafts)
		c.drafts = slices.DeleteFunc(c.drafts, func(d api.AgentWorkerDraft) bool { return d.WorkerID == jobID })
		changed := len(c.drafts) != before
		if !c.running {
			changed = c.dropJobItems(jobID) || changed
		}
		return changed
	})
}

// sessionRetitled updates a chat's title.
func (t *Tracker) sessionRetitled(ctx context.Context, sessionID, title string) {
	ref, ok := t.chat(ctx, sessionID)
	if !ok || ref.JobID != "" {
		return
	}
	ref.Title = title
	t.mutate(ctx, ref, func(*chatPresence) bool { return false })
}

// Snapshot returns every chat with presence in a project.
func (t *Tracker) Snapshot(projectID string) api.AgentPresenceSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := api.AgentPresenceSnapshot{ProjectID: projectID, Sessions: []api.AgentSessionPresence{}}
	p := t.projects[projectID]
	if p == nil {
		return out
	}
	out.Revision = p.revision
	ids := make([]string, 0, len(p.chats))
	for id, c := range p.chats {
		if !c.empty() {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		out.Sessions = append(out.Sessions, p.chats[id].wire())
	}
	return out
}

// mutate applies change to one chat and publishes the chat when it changed.
func (t *Tracker) mutate(ctx context.Context, ref ChatRef, change func(*chatPresence) bool) {
	t.mu.Lock()
	p := t.projects[ref.ProjectID]
	if p == nil {
		p = &projectPresence{chats: make(map[string]*chatPresence)}
		t.projects[ref.ProjectID] = p
	}
	if ref.Title != "" {
		if t.titles == nil || (len(t.titles) >= maxTitles && t.titles[ref.SessionID] == "") {
			t.titles = make(map[string]string)
		}
		t.titles[ref.SessionID] = ref.Title
	}
	c := p.chats[ref.SessionID]
	created := c == nil
	if created {
		c = &chatPresence{sessionID: ref.SessionID}
		p.chats[ref.SessionID] = c
	}
	title := t.titles[ref.SessionID]
	retitled := title != "" && title != c.title
	if retitled {
		c.title = title
	}
	if !change(c) && !(retitled && !c.empty()) {
		if created && c.empty() && !c.running {
			delete(p.chats, ref.SessionID)
		}
		t.mu.Unlock()
		return
	}
	p.revision++
	ev := api.AgentPresenceEvent{ProjectID: ref.ProjectID, SessionID: ref.SessionID, Revision: p.revision, Presence: c.wire()}
	if c.empty() && !c.running {
		delete(p.chats, ref.SessionID)
	}
	t.mu.Unlock()
	if t.publisher != nil {
		t.publisher.PublishAgentPresence(ctx, ev)
	}
}

func (t *Tracker) chat(ctx context.Context, sessionID string) (ChatRef, bool) {
	if t == nil || t.chats == nil || strings.TrimSpace(sessionID) == "" {
		return ChatRef{}, false
	}
	ref, ok := t.chats.Chat(ctx, sessionID)
	if !ok || ref.ProjectID == "" || ref.SessionID == "" {
		return ChatRef{}, false
	}
	return ref, true
}

func (t *Tracker) id(kind string) string {
	t.nextID++
	return kind + "-" + strconv.FormatUint(t.nextID, 36)
}

// anchorDocument anchors spans in the document state the item was recorded
// against. Without a document, or when anchoring fails, the item keeps lines only.
func (t *Tracker) anchorDocument(ctx context.Context, projectID string, d Document, spans []Span) (Anchored, bool) {
	if t.anchors == nil || d.ID == "" {
		return Anchored{}, false
	}
	anchored, err := t.anchors.AnchorSpans(ctx, projectID, d.ID, d.Revision, spans)
	if err != nil || len(anchored.Spans) != len(spans) {
		return Anchored{}, false
	}
	return anchored, true
}

func (c *chatPresence) empty() bool {
	return len(c.activities) == 0 && len(c.reads) == 0 && len(c.intents) == 0 && len(c.drafts) == 0
}

func (c *chatPresence) dropJobItems(jobID string) bool {
	before := len(c.activities) + len(c.reads) + len(c.intents)
	c.activities = slices.DeleteFunc(c.activities, func(a api.AgentActivity) bool { return a.WorkerID == jobID })
	c.reads = slices.DeleteFunc(c.reads, func(r *anchoredRead) bool { return r.wire.WorkerID == jobID })
	c.intents = slices.DeleteFunc(c.intents, func(i api.AgentIntent) bool { return i.WorkerID == jobID })
	return len(c.activities)+len(c.reads)+len(c.intents) != before
}

func (c *chatPresence) wire() api.AgentSessionPresence {
	out := api.AgentSessionPresence{SessionID: c.sessionID, Title: c.title, Turn: c.turn,
		Activities: slices.Clone(c.activities), Intents: slices.Clone(c.intents), WorkerDrafts: slices.Clone(c.drafts),
		Reads: make([]api.AgentRead, 0, len(c.reads))}
	if out.Activities == nil {
		out.Activities = []api.AgentActivity{}
	}
	if out.Intents == nil {
		out.Intents = []api.AgentIntent{}
	}
	if out.WorkerDrafts == nil {
		out.WorkerDrafts = []api.AgentWorkerDraft{}
	}
	for _, r := range c.reads {
		out.Reads = append(out.Reads, r.wire)
	}
	return out
}

func validTarget(target Target) bool {
	return strings.TrimSpace(target.RootID) != "" && strings.TrimSpace(target.Path) != ""
}

func boundedSpans(spans []Span, limit int) []Span {
	valid := make([]Span, 0, min(len(spans), limit))
	for _, span := range spans {
		if len(valid) == limit {
			break
		}
		if span.StartLine >= 1 && span.EndLine >= span.StartLine {
			valid = append(valid, span)
		}
	}
	return valid
}

func wireRanges(spans []Span, anchored Anchored, anchoredOK bool) []api.AgentTextRange {
	out := make([]api.AgentTextRange, len(spans))
	for i, span := range spans {
		out[i] = api.AgentTextRange{StartLine: span.StartLine, EndLine: span.EndLine, StartCharacter: span.StartCharacter, EndCharacter: span.EndCharacter}
		if anchoredOK {
			out[i].Anchor, out[i].Head = anchored.Spans[i].Anchor, anchored.Spans[i].Head
		}
	}
	return out
}

// boundReads drops the oldest reads beyond the per-chat read and range budgets.
func boundReads(reads []*anchoredRead) []*anchoredRead {
	reads = keepNewest(reads, maxReads)
	total := 0
	for i := len(reads) - 1; i >= 0; i-- {
		total += len(reads[i].wire.Ranges)
		if total > maxChatRanges {
			return reads[i+1:]
		}
	}
	return reads
}

func keepNewest[T any](items []T, limit int) []T {
	if len(items) <= limit {
		return items
	}
	return slices.Clone(items[len(items)-limit:])
}

// anchorPath anchors spans in the current head of a path's existing document.
func (t *Tracker) anchorPath(ctx context.Context, projectID string, target Target, spans []Span) (Anchored, bool) {
	if t.anchors == nil {
		return Anchored{}, false
	}
	anchored, ok, err := t.anchors.AnchorPathSpans(ctx, projectID, target, spans)
	if err != nil || !ok || len(anchored.Spans) != len(spans) {
		return Anchored{}, false
	}
	return anchored, true
}
