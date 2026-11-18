package sweeps

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/test/security"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// conformanceEffects observes host state changes around one read. The sweep
// sends reads one at a time, so a change between mark and since belongs to the
// read in between or to background work the fixtures started.
type conformanceEffects interface {
	mark(ctx context.Context) (int64, error)
	// since lists what changed after mark.
	since(ctx context.Context, mark int64) ([]string, error)
	// tables digests each table's content, so a commit can be attributed.
	tables(ctx context.Context) (map[string]string, error)
}

// readEffect is the work a read may start: the tables it writes and the
// event topics it publishes.
type readEffect struct {
	tables, topics []string
}

// hostReadEffects are the effects the host's reads may declare.
var hostReadEffects = map[string]readEffect{
	"file_briefing": {
		tables: []string{"file_briefings", "briefing_epochs", "briefing_manifests", "briefing_results", "briefing_records", "briefing_record_sections", "briefing_targets", "briefing_checkpoint_pins"},
		topics: []string{string(wire.EventTopicFileBriefing)},
	},
}

// permit removes the changes the effect accounts for. A commit is permitted
// only when every table it changed belongs to the effect.
func (e readEffect) permit(changes []string, before, after map[string]string) []string {
	var out []string
	for _, change := range changes {
		if topic, ok := strings.CutPrefix(change, "event "); ok && slices.Contains(e.topics, topic) {
			continue
		}
		if change == "store commit" {
			var others []string
			for _, table := range slices.Sorted(maps.Keys(after)) {
				if after[table] != before[table] && !slices.Contains(e.tables, table) {
					others = append(others, table)
				}
			}
			if len(others) == 0 {
				continue
			}
			change = "store commit to " + strings.Join(others, ", ")
		}
		out = append(out, change)
	}
	return out
}

// sweepReads sends every GET with fixture parameters, parents before children,
// reports every read that changes host state, and binds the ids each list
// answers so later operations address real resources. The host is quiescent
// before each read, and the work a read starts is drained into its
// measurement, so one read is one exact observation.
func (s *conformanceSweep) sweepReads(ctx context.Context) {
	var reads []*conformanceOp
	for _, op := range s.ops {
		if op.Method == http.MethodGet {
			reads = append(reads, op)
		}
	}
	sort.SliceStable(reads, func(i, j int) bool { return len(reads[i].PathParams) < len(reads[j].PathParams) })
	for _, op := range reads {
		req, err := s.successRequest(ctx, op)
		if err != nil {
			s.findings.add(ruleOperationsSucceed, op.ID, "read", "could not prepare: %v", err)
			continue
		}
		req.probe = "read"
		var ex conformanceExchange
		if s.readObserved[op.ID] {
			ex = s.send(ctx, req)
		} else {
			var changes []string
			ex, changes = s.observedRead(ctx, req)
			if len(changes) > 0 {
				s.findings.add(ruleReadsHaveNoEffects, op.ID, req.probe, "changed host state: %s", strings.Join(changes, ", "))
			}
		}
		if ex.status/100 == 2 {
			s.readObserved[op.ID] = true
			s.discover(op, ex)
		}
	}
}

// observedRead sends one read and names what changed while it ran.
func (s *conformanceSweep) observedRead(ctx context.Context, req conformanceRequest) (conformanceExchange, []string) {
	if s.effects == nil {
		return s.send(ctx, req), nil
	}
	mark, err := s.effects.mark(ctx)
	if err != nil {
		s.findings.add(ruleReadsHaveNoEffects, req.op.ID, req.probe, "could not observe host state after %s: %v", s.lastMutation, err)
		return s.send(ctx, req), nil
	}
	effect, declared := s.readEffects[req.op.ReadEffect]
	if req.op.ReadEffect != "" && !declared {
		s.findings.add(ruleReadsHaveNoEffects, req.op.ID, req.probe, "declares read effect %q, which names no known work", req.op.ReadEffect)
	}
	var before map[string]string
	if declared {
		if before, err = s.effects.tables(ctx); err != nil {
			s.findings.add(ruleReadsHaveNoEffects, req.op.ID, req.probe, "could not digest host state: %v", err)
			return s.send(ctx, req), nil
		}
	}
	ex := s.send(ctx, req)
	changes, err := s.effects.since(ctx, mark)
	if err != nil {
		s.findings.add(ruleReadsHaveNoEffects, req.op.ID, req.probe, "could not observe host state: %v", err)
		return ex, nil
	}
	if declared {
		after, err := s.effects.tables(ctx)
		if err != nil {
			s.findings.add(ruleReadsHaveNoEffects, req.op.ID, req.probe, "could not digest host state: %v", err)
			return ex, nil
		}
		changes = effect.permit(changes, before, after)
	}
	slices.Sort(changes)
	return ex, slices.Compact(changes)
}

// discover binds the first member of a list answer to every path parameter
// addressed directly below the list's path. A member is named by its `id`, or
// by a property named like the parameter.
func (s *conformanceSweep) discover(op *conformanceOp, ex conformanceExchange) {
	names := s.children[op.Path]
	if len(names) == 0 {
		return
	}
	member := firstListMember(op.Path, ex.body)
	if member == nil {
		return
	}
	for _, name := range names {
		prefix := op.Path + "/{" + name + "}"
		value, _ := member[name].(string)
		if id, ok := member["id"].(string); ok && value == "" {
			value = id
		}
		if name == "id" {
			s.values.bind(prefix, "", value)
		} else {
			s.values.bind(prefix, name, value)
		}
	}
}

// firstListMember returns the first object of the list envelope's array,
// preferring the array named for the collection's last segment.
func firstListMember(path string, body []byte) map[string]any {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	segments := strings.Split(path, "/")
	want := strings.ReplaceAll(segments[len(segments)-1], "-", "_")
	keys := make([]string, 0, len(envelope))
	for key := range envelope {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i] == want && keys[j] != want })
	for _, key := range keys {
		list, ok := envelope[key].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		if member, ok := list[0].(map[string]any); ok {
			return member
		}
	}
	return nil
}

// hostEffects observes store writes and events after detached work settles.
type hostEffects struct {
	conn   *sql.Conn
	hub    *events.MemoryHub
	events <-chan wire.EventEnvelope
	pump   *eventPump
	settle func(context.Context)
	// projectDir is the watched fixture tree whose change pipeline must drain.
	projectDir string
	// quietGrace is how long the tree's changes must stay idle before the host
	// counts as settled.
	quietGrace time.Duration
	// barrierStep numbers barrier files; barrierName is the one in place.
	barrierStep int
	barrierName string
	// textColumns lists each table's text columns, read once.
	textColumns map[string][]string
	// silent is set once the watcher stops reporting; later reads fail at once.
	silent error
	// unmatched keeps the latest changes a barrier saw that were not its own.
	unmatched []string
}

// conformanceDebounce is the change coalesce window the sweep's host runs
// with; every read waits on it twice.
const conformanceDebounce = time.Millisecond

// watcherQuietBound caps one quiet wait so a continuously changing tree fails
// the read it interferes with instead of hanging the sweep.
const watcherQuietBound = 10 * time.Second

// watcherBarrierFile proves the watcher has delivered every earlier change.
const watcherBarrierFile = "conformance-barrier.txt"

// settlePasses bounds the convergence loop against continuous producers.
const settlePasses = 64

// openConformanceEffects observes a host running with the given change
// debounce. The quiet grace is two windows, so a write that reached the
// platform stream late still lands in the same measurement.
func openConformanceEffects(t *testing.T, h *wiring.Harness, hub *events.MemoryHub, projectDir string, debounce time.Duration) *hostEffects {
	t.Helper()
	ctx := t.Context()
	database, err := db.OpenReadOnly(ctx, h.DatabasePath())
	testutil.FailErr(t, "open a read-only store connection", err)
	conn, err := database.Conn(ctx)
	testutil.FailErr(t, "pin the data_version connection", err)
	ch, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to every event", err)
	t.Cleanup(func() {
		unsubscribe()
		_ = conn.Close()
		_ = database.Close()
	})
	pump := newEventPump(ch)
	return &hostEffects{conn: conn, hub: hub, events: pump.out, pump: pump, settle: hostSettle(h), projectDir: projectDir, quietGrace: 2 * debounce}
}

// eventPump keeps reading a subscription so a burst between two reads never
// fills the hub's buffer, which would drop it. It is the only reader, so a
// take collects every event the hub has delivered, including any still in
// the subscription's buffer.
type eventPump struct {
	out  chan wire.EventEnvelope
	take chan chan pumpTake
}

type pumpTake struct {
	events  []wire.EventEnvelope
	dropped bool
}

func newEventPump(in <-chan wire.EventEnvelope) *eventPump {
	p := &eventPump{out: make(chan wire.EventEnvelope), take: make(chan chan pumpTake)}
	go p.run(in)
	return p
}

func (p *eventPump) run(in <-chan wire.EventEnvelope) {
	var queue []wire.EventEnvelope
	dropped, closed := false, false
	receive := func(env wire.EventEnvelope, ok bool) {
		if !ok {
			in, dropped = nil, true
			return
		}
		queue = append(queue, env)
	}
	for {
		if dropped && len(queue) == 0 && !closed {
			close(p.out)
			closed = true
		}
		var send chan wire.EventEnvelope
		var next wire.EventEnvelope
		if len(queue) > 0 {
			send, next = p.out, queue[0]
		}
		select {
		case env, ok := <-in:
			receive(env, ok)
		case send <- next:
			queue = queue[1:]
		case reply := <-p.take:
			for pending := true; pending && in != nil; {
				select {
				case env, ok := <-in:
					receive(env, ok)
				default:
					pending = false
				}
			}
			reply <- pumpTake{events: queue, dropped: dropped}
			queue = nil
		}
	}
}

// collect takes every delivered event without waiting for more.
func (p *eventPump) collect() pumpTake {
	reply := make(chan pumpTake, 1)
	p.take <- reply
	return <-reply
}

func (e *hostEffects) mark(ctx context.Context) (int64, error) {
	if _, err := os.Stat(e.projectDir); err != nil {
		return 0, fmt.Errorf("the fixture project folder is gone: %w", err)
	}
	if e.silent != nil {
		return 0, e.silent
	}
	if err := e.awaitWatcherBarrier(ctx); err != nil {
		e.silent = err
		return 0, err
	}
	e.quiesce(ctx)
	e.drain()
	return e.dataVersion(ctx)
}

// awaitWatcherBarrier writes a file and waits for the watcher to report it. The
// platform stream is ordered, so every earlier write has then reached the
// pipeline, however long a contended host took to deliver it. A rewrite leaves a
// later report outstanding, which the read that follows would be charged with, so
// only a round that wrote once proves the pipeline empty.
func (e *hostEffects) awaitWatcherBarrier(ctx context.Context) error {
	patience := time.Second
	for range watcherBarrierRounds {
		rewrote, err := e.watcherBarrierRound(ctx, patience)
		if err != nil || !rewrote {
			return err
		}
		e.quiesce(ctx)
		e.drain()
		patience *= 2
	}
	return fmt.Errorf("the watcher reported none of %d barrier rounds in %s; it reported %v; coverage %+v", watcherBarrierRounds, e.projectDir, e.unmatched, repochange.Coverage(e.projectDir))
}

// watcherBarrierRounds bounds the rewrites of an unanswered barrier, so a
// watcher that stopped reporting fails each read instead of hanging the sweep.
const watcherBarrierRounds = 6

func (e *hostEffects) watcherBarrierRound(ctx context.Context, patience time.Duration) (bool, error) {
	if err := e.placeBarrier(); err != nil {
		return false, err
	}
	retry := time.NewTicker(patience)
	defer retry.Stop()
	rewrote := false
	for {
		select {
		case envelope, ok := <-e.events:
			if !ok {
				return rewrote, errors.New("event stream dropped while awaiting the watcher barrier")
			}
			var changed wire.SourceChangesEvent
			if envelope.Topic != wire.EventTopicSourceChanged || json.Unmarshal(envelope.Data, &changed) != nil {
				continue
			}
			if slices.ContainsFunc(changed.Changes, func(c wire.SourceChange) bool { return c.Path == e.barrierName }) {
				return rewrote, nil
			}
			e.noteUnmatched(changed)
			if changed.Resync {
				rewrote = true
				if err := e.placeBarrier(); err != nil {
					return rewrote, err
				}
			}
		case <-retry.C:
			return true, nil
		case <-ctx.Done():
			return rewrote, ctx.Err()
		}
	}
}

// placeBarrier removes the previous barrier file and creates a new one. A new
// file is a structural change every platform's watcher reports; an in-place
// rewrite is not.
func (e *hostEffects) placeBarrier() error {
	if e.barrierName != "" {
		if err := os.Remove(filepath.Join(e.projectDir, e.barrierName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	e.barrierStep++
	e.barrierName = fmt.Sprintf("conformance-barrier-%d.txt", e.barrierStep)
	return os.WriteFile(filepath.Join(e.projectDir, e.barrierName), []byte("barrier\n"), 0o644)
}

func (e *hostEffects) since(ctx context.Context, mark int64) ([]string, error) {
	e.quiesce(ctx)
	changes := e.drain()
	version, err := e.dataVersion(ctx)
	if err != nil {
		return nil, err
	}
	if version != mark {
		changes = append(changes, "store commit")
	}
	return changes, nil
}

// quiesce waits until the host is still: pending change bursts are delivered,
// the fixture tree stays idle, detached work has drained, the outbox is empty,
// and a whole pass committed nothing. A filesystem change travels platform
// stream → debounce timer → watcher batch → ledger and outbox commits, and
// only the last legs are joined by WaitForBackground, so one pass can finish
// while a change is still in flight; a second pass that moves nothing proves
// the pipeline is empty.
func (e *hostEffects) quiesce(ctx context.Context) {
	version, _ := e.dataVersion(ctx)
	for pass := 0; pass < settlePasses; pass++ {
		repochange.ResetDebouncerForTest(ctx)
		repochange.WaitQuiet(ctx, e.projectDir, e.quietGrace, watcherQuietBound)
		e.settle(ctx)
		var pending int64
		if err := e.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_outbox").Scan(&pending); err == nil && pending > 0 {
			continue
		}
		next, err := e.dataVersion(ctx)
		if err != nil || next == version {
			break
		}
		version = next
	}
	e.hub.FlushDebounced()
}

// tables digests every table's rows.
func (e *hostEffects) tables(ctx context.Context) (map[string]string, error) {
	rows, err := e.conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	digests := make(map[string]string, len(names))
	for _, name := range names {
		digest, err := e.tableDigest(ctx, name)
		if err != nil {
			return nil, err
		}
		digests[name] = digest
	}
	return digests, nil
}

func (e *hostEffects) tableDigest(ctx context.Context, table string) (string, error) {
	rows, err := e.conn.QueryContext(ctx, `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	values := make([]sql.RawBytes, len(columns))
	targets := make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return "", err
		}
		for _, v := range values {
			_, _ = fmt.Fprintf(h, "%d:%s", len(v), v)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), rows.Err()
}

func (e *hostEffects) dataVersion(ctx context.Context) (int64, error) {
	var version int64
	err := e.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version)
	return version, err
}

// drain empties the subscription without blocking and names each event.
func (e *hostEffects) drain() []string {
	taken := e.pump.collect()
	published := make([]string, 0, len(taken.events)+1)
	for _, env := range taken.events {
		published = append(published, fmt.Sprintf("event %s", env.Topic))
	}
	if taken.dropped {
		published = append(published, "event stream dropped")
	}
	return published
}

// settleSourceWatcher waits until the project's watcher has delivered every
// filesystem change the fixtures made, so reads start with none in flight.
func settleSourceWatcher(t *testing.T, settle func(context.Context), hub *events.MemoryHub, projectDir string) {
	t.Helper()
	settle(t.Context())
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to source changes", err)
	defer unsubscribe()
	barrier := watcherBarrierFile
	barrierPath := filepath.Join(projectDir, barrier)
	writeBarrier := func(step int) {
		testutil.FailErr(t, "write the watcher barrier", os.WriteFile(barrierPath, fmt.Appendf(nil, "barrier-%d\n", step), 0o644))
	}
	step := 1
	writeBarrier(step)
	retry := time.NewTicker(time.Second)
	defer retry.Stop()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case envelope := <-ch:
			var changed wire.SourceChangesEvent
			if envelope.Topic != wire.EventTopicSourceChanged || json.Unmarshal(envelope.Data, &changed) != nil {
				continue
			}
			if slices.ContainsFunc(changed.Changes, func(c wire.SourceChange) bool { return c.Path == barrier }) {
				return
			}
			if changed.Resync {
				step++
				writeBarrier(step)
			}
		case <-t.Context().Done():
			t.Fatal("the source watcher did not report the barrier file")
		case <-retry.C:
			step++
			writeBarrier(step)
		case <-deadline:
			t.Fatal("the source watcher did not report the barrier file")
		}
	}
}

// TestConformanceEffectsSettleLandedWritesBeforeMark proves a filesystem
// change that landed before mark is never charged to the read that follows.
// The watcher delivers a write through a debounce timer and a batch queue that
// WaitForBackground does not join, so a mark taken right after the write must
// wait for that pipeline to drain, or a slow read observes the source_changed
// event and the ledger commits as its own side effects.
func TestConformanceEffectsSettleLandedWritesBeforeMark(t *testing.T) {
	h := wiring.BuildForTest(t)
	hub := h.MemoryHub()
	if hub == nil {
		t.Fatal("the effects observer needs the in-memory event hub")
	}
	projectDir := h.ProjectDir(t, "effects")
	testutil.FailErr(t, "write fixture file", os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("effects\n"), 0o644))
	security.CreateTestProjectHTTP(t, h.Server, projectDir)
	settleSourceWatcher(t, h.Server.WaitForBackground, hub, projectDir)
	ctx := t.Context()
	published, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to source changes", err)
	defer unsubscribe()
	effects := openConformanceEffects(t, h, hub, projectDir, repochange.DefaultDebounce)

	const landed = "landed.txt"
	for step := range 3 {
		testutil.FailErr(t, "write the landed file",
			os.WriteFile(filepath.Join(projectDir, landed), []byte{byte('a' + step), '\n'}, 0o644))
		mark, err := effects.mark(ctx)
		testutil.FailErr(t, "mark host state", err)
		changes, err := effects.since(ctx, mark)
		testutil.FailErr(t, "observe host state", err)
		if len(changes) > 0 {
			t.Fatalf("step %d: a write that landed before mark was charged to the read: %v", step, changes)
		}
	}
	if !sourceChangeNamed(published, landed) {
		t.Fatal("the watcher never reported the landed file, so the settle was not exercised")
	}
}

// sourceChangeNamed reports whether an already delivered source_changed event
// names path, so a passing settle proves draining rather than a blind watcher.
func sourceChangeNamed(published <-chan wire.EventEnvelope, path string) bool {
	for {
		select {
		case envelope := <-published:
			var changed wire.SourceChangesEvent
			if envelope.Topic != wire.EventTopicSourceChanged || json.Unmarshal(envelope.Data, &changed) != nil {
				continue
			}
			if slices.ContainsFunc(changed.Changes, func(c wire.SourceChange) bool { return c.Path == path }) {
				return true
			}
		default:
			return false
		}
	}
}

// keyRows counts, per table, the rows any text column of which equals key,
// once the work earlier requests started has settled.
func (e *hostEffects) keyRows(ctx context.Context, key string) (map[string]int, error) {
	e.quiesce(ctx)
	if e.textColumns == nil {
		columns, err := storeTextColumns(ctx, e.conn)
		if err != nil {
			return nil, err
		}
		e.textColumns = columns
	}
	counts := map[string]int{}
	for table, columns := range e.textColumns {
		conditions := make([]string, len(columns))
		args := make([]any, len(columns))
		for i, column := range columns {
			conditions[i] = fmt.Sprintf("%q = ?", column)
			args[i] = key
		}
		var n int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %q WHERE %s", table, strings.Join(conditions, " OR "))
		if err := e.conn.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		if n > 0 {
			counts[table] = n
		}
	}
	return counts, nil
}

// storeTextColumns lists the text columns of every ordinary table.
func storeTextColumns(ctx context.Context, conn *sql.Conn) (map[string][]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT m.name, p.name FROM sqlite_schema m JOIN pragma_table_info(m.name) p
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND upper(p.type) LIKE '%TEXT%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		out[table] = append(out[table], column)
	}
	if len(out) == 0 && rows.Err() == nil {
		return nil, errors.New("the store has no text columns")
	}
	return out, rows.Err()
}

// hostSettle waits for the server's detached work and for briefing
// generations, which run on their own scheduler.
func hostSettle(h *wiring.Harness) func(context.Context) {
	return func(ctx context.Context) {
		h.Server.WaitForBackground(ctx)
		if briefings := h.Server.Sources.Briefings; briefings != nil && briefings.Engine != nil && briefings.Engine.Host() != nil {
			briefings.Engine.Host().Scheduler.Wait(ctx)
		}
	}
}

func (e *hostEffects) noteUnmatched(changed wire.SourceChangesEvent) {
	for _, change := range changed.Changes {
		e.unmatched = append(e.unmatched, change.RootID+":"+change.Path)
	}
	if changed.Resync {
		e.unmatched = append(e.unmatched, "resync")
	}
	if len(e.unmatched) > 8 {
		e.unmatched = e.unmatched[len(e.unmatched)-8:]
	}
}
