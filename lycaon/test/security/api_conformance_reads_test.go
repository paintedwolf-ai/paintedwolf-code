package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
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
		params, _ := s.values.pathParams(op)
		req := conformanceRequest{op: op, probe: "read", params: params, query: s.values.requiredQuery(op)}
		ex, changes := s.observedRead(ctx, req)
		if len(changes) > 0 {
			s.findings.add(ruleReadsHaveNoEffects, op.ID, req.probe, "changed host state: %s", strings.Join(changes, ", "))
		}
		if ex.status/100 == 2 {
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
		s.findings.add(ruleReadsHaveNoEffects, req.op.ID, req.probe, "could not observe host state: %v", err)
		return s.send(ctx, req), nil
	}
	ex := s.send(ctx, req)
	changes, err := s.effects.since(ctx, mark)
	if err != nil {
		s.findings.add(ruleReadsHaveNoEffects, req.op.ID, req.probe, "could not observe host state: %v", err)
		return ex, nil
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
	settle func(context.Context)
}

func openConformanceEffects(t *testing.T, h *wiring.Harness, hub *events.MemoryHub) *hostEffects {
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
	return &hostEffects{conn: conn, hub: hub, events: ch, settle: h.Server.WaitForBackground}
}

func (e *hostEffects) mark(ctx context.Context) (int64, error) {
	e.quiesce(ctx)
	e.drain()
	return e.dataVersion(ctx)
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

// quiesce waits for detached work, then publishes debounced events.
func (e *hostEffects) quiesce(ctx context.Context) {
	e.settle(ctx)
	e.hub.FlushDebounced()
}

func (e *hostEffects) dataVersion(ctx context.Context) (int64, error) {
	var version int64
	err := e.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version)
	return version, err
}

// drain empties the subscription without blocking and names each event.
func (e *hostEffects) drain() []string {
	var published []string
	for {
		select {
		case env, ok := <-e.events:
			if !ok {
				return append(published, "event stream dropped")
			}
			published = append(published, fmt.Sprintf("event %s", env.Topic))
		default:
			return published
		}
	}
}

// settleSourceWatcher waits until the project's watcher has delivered every
// filesystem change the fixtures made. Watcher events arrive in order, so the
// report of a file written last follows every earlier one; the reads then start
// from a host with no filesystem change still in flight.
func settleSourceWatcher(t *testing.T, hub *events.MemoryHub, projectDir string) {
	t.Helper()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to source changes", err)
	defer unsubscribe()
	const barrier = "conformance-barrier.txt"
	testutil.FailErr(t, "write the watcher barrier", os.WriteFile(filepath.Join(projectDir, barrier), []byte("barrier\n"), 0o644))
	deadline := time.After(30 * time.Second)
	for {
		select {
		case envelope := <-ch:
			if envelope.Topic != wire.EventTopicSourceChanged {
				continue
			}
			var changed wire.SourceChangesEvent
			if json.Unmarshal(envelope.Data, &changed) != nil {
				continue
			}
			if changed.Resync {
				return
			}
			for _, change := range changed.Changes {
				if change.Path == barrier {
					return
				}
			}
		case <-deadline:
			t.Fatal("the source watcher did not report the barrier file")
		}
	}
}
