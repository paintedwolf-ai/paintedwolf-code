package sweeps

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// idempotencyKey is the body property that makes an operation replayable.
const idempotencyKey = "operation_id"

// idempotentReplays is how many copies of one keyed request race each other.
const idempotentReplays = 8

// conformanceKeyRows counts, per table, the stored rows that name a key.
type conformanceKeyRows interface {
	keyRows(ctx context.Context, key string) (map[string]int, error)
}

// keyed reports whether op's JSON body carries an operation_id.
func (op *conformanceOp) keyed() bool {
	return op.JSONBody != nil && refValue(op.JSONBody.Properties[idempotencyKey]) != nil
}

// checkIdempotent replays a keyed request that succeeded: twice in turn, then
// from idempotentReplays goroutines at once. Every replay answers the first
// answer's status and names the same resource, and stores nothing more; the
// same key with a different body answers 409 idempotency_conflict. A replay
// may report the resource's current state, so only identifiers must match.
func (s *conformanceSweep) checkIdempotent(ctx context.Context, first conformanceExchange) {
	op := first.req.op
	key := bodyKey(first.req.body)
	if key == "" {
		s.findings.add(ruleKeyedOperationsAreIdempotent, op.ID, "replay", "the success request carries no %s", idempotencyKey)
		return
	}
	before, err := s.rowsNaming(ctx, key)
	if err != nil {
		s.findings.add(ruleKeyedOperationsAreIdempotent, op.ID, "replay", "could not count stored rows: %v", err)
		return
	}
	replay := first.req
	replay.probe = "replay"
	for range 2 {
		s.compareReplay(first, s.send(ctx, replay))
	}
	replay.probe = "concurrent replay"
	answers := make([]conformanceExchange, idempotentReplays)
	var wg sync.WaitGroup
	for i := range answers {
		wg.Go(func() { answers[i] = s.send(ctx, replay) })
	}
	wg.Wait()
	for _, ex := range answers {
		s.compareReplay(first, ex)
	}
	if after, err := s.rowsNaming(ctx, key); err != nil {
		s.findings.add(ruleKeyedOperationsAreIdempotent, op.ID, "replay", "could not count stored rows: %v", err)
	} else if !maps.Equal(before, after) {
		s.findings.add(ruleKeyedOperationsAreIdempotent, op.ID, "replay", "replays changed the rows naming the key: %v, then %v", before, after)
	}
	changed := changedBodies(op.JSONBody, first.req.body)
	if len(changed) == 0 {
		// A body of nothing but its key cannot be sent changed.
		s.findings.note("%s: changed body: the body carries only its key", op.ID)
		return
	}
	// Some properties are preconditions a retry may refresh; the key must
	// still guard the operation's input, so some change must be refused.
	var outcomes []string
	for _, name := range slices.Sorted(maps.Keys(changed)) {
		conflict := first.req
		conflict.probe, conflict.body = "changed body", changed[name]
		ex := s.send(ctx, conflict)
		if ex.status == http.StatusConflict && ex.code() == string(wire.ApiErrorCodeIdempotencyConflict) {
			return
		}
		outcomes = append(outcomes, name+": "+ex.outcome())
	}
	s.findings.add(ruleKeyedOperationsAreIdempotent, op.ID, "changed body", "no change answered 409 %s (%s)", wire.ApiErrorCodeIdempotencyConflict, strings.Join(outcomes, ", "))
}

func (s *conformanceSweep) compareReplay(first, replay conformanceExchange) {
	if replay.status != first.status {
		s.findings.add(ruleKeyedOperationsAreIdempotent, first.req.op.ID, replay.req.probe, "answered %s, first answer was %s", replay.outcome(), first.outcome())
		return
	}
	was, now := answerIdentifiers(first.body), answerIdentifiers(replay.body)
	for _, name := range slices.Sorted(maps.Keys(was)) {
		if now[name] != was[name] {
			s.findings.add(ruleKeyedOperationsAreIdempotent, first.req.op.ID, replay.req.probe, "answered %s %q, first answer named %q", name, now[name], was[name])
		}
	}
}

// answerIdentifiers is every top-level id the answer names.
func answerIdentifiers(body []byte) map[string]string {
	var fields map[string]any
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	out := map[string]string{}
	for name, value := range fields {
		if id, ok := value.(string); ok && (name == "id" || strings.HasSuffix(name, "_id")) {
			out[name] = id
		}
	}
	return out
}

func (s *conformanceSweep) rowsNaming(ctx context.Context, key string) (map[string]int, error) {
	if s.keyRows == nil {
		return nil, fmt.Errorf("the sweep has no store to count")
	}
	return s.keyRows.keyRows(ctx, key)
}

func bodyKey(body []byte) string {
	var fields map[string]any
	if json.Unmarshal(body, &fields) != nil {
		return ""
	}
	key, _ := fields[idempotencyKey].(string)
	return key
}

// changedBodies alters body one property at a time, for each property its
// schema leaves free: a plain string, a UUID, a boolean or a number.
func changedBodies(schema *openapi3.Schema, body []byte) map[string][]byte {
	var fields map[string]any
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	out := map[string][]byte{}
	for name, value := range fields {
		prop := refValue(schema.Properties[name])
		if name == idempotencyKey || prop == nil || len(prop.Enum) > 0 || prop.Const != nil {
			continue
		}
		var changed any
		switch v := value.(type) {
		case string:
			switch {
			case prop.Format == "uuid":
				changed = uuid.NewString()
			case prop.Format == "" && prop.Pattern == "":
				changed = v + "-changed"
			}
		case bool:
			changed = !v
		case float64:
			if prop.Max == nil || v+1 <= *prop.Max {
				changed = v + 1
			}
		}
		if changed == nil {
			continue
		}
		copied := maps.Clone(fields)
		copied[name] = changed
		if raw, err := json.Marshal(copied); err == nil {
			out[name] = raw
		}
	}
	return out
}
