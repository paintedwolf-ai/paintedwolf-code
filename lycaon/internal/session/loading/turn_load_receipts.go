package loading

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// neededForTurn marks recall needed once the session has compacted messages or a
// compaction epoch, keeping the recall guide attached.
func neededForTurn(loaded []string, history []api.Message, historyEpoch string) []string {
	if slices.Contains(loaded, "recall") || !historyNeedsRecall(history, historyEpoch) {
		return loaded
	}
	out := append(append([]string(nil), loaded...), "recall")
	sort.Strings(out)
	return out
}

func historyNeedsRecall(history []api.Message, historyEpoch string) bool {
	if strings.TrimSpace(historyEpoch) != "" {
		return true
	}
	for _, msg := range history {
		if msg.CompactionCheckpoint || msg.CompactedChunk != nil {
			return true
		}
	}
	return false
}

// ResolveToolRequest ranks requestable schemas; the handler records the final activation.
func (m *Service) ResolveToolRequest(ctx context.Context, tctx tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
	catalog, err := turnload.LoadCatalog()
	if err != nil {
		return turnload.RequestOutcome{Need: need, Exact: turnload.ExactNames(need, cards), Abstained: true, Reason: "decision catalog unavailable", Failure: turnload.RankingUnavailable}
	}
	sess := m.toolSession(ctx, tctx)
	finishDeciding := m.beginDeciding(ctx, sess, tctx.Identity.SessionID, api.TurnLoadTriggerRequest, tctx.Identity.ToolCallID)
	outcome := turnload.ResolveRequest(ctx, m.Decider(), catalog.Request, need, cards)
	finishDeciding()
	return outcome
}

// RecordToolRequest persists the standing surface after filtering and activation,
// so receipts and restart recovery agree with the next model call.
func (m *Service) RecordToolRequest(ctx context.Context, tctx tools.ToolContext, outcome turnload.RequestOutcome, result turnload.RequestToolsResult, elapsed time.Duration) {
	sess := m.toolSession(ctx, tctx)
	if sess == nil {
		return
	}
	userChars := 900
	if catalog, err := turnload.LoadCatalog(); err == nil {
		userChars = catalog.State.UserTextChars
	}
	host := promptunit.HostWorker
	if guard.IsCoordinatorProfile(tctx.Identity.Agent) {
		host = promptunit.HostCoordinator
	}
	stateSurface := strings.TrimSpace(tctx.Turn.TurnSurfaceID)
	if stateSurface == "" {
		stateSurface = strings.TrimSpace(tctx.Identity.Agent)
	}
	state := turnload.State{Host: string(host), User: turnload.BoundUser(outcome.Need, userChars), Surface: stateSurface}
	m.recordTurnLoad(ctx, sess, tctx.Identity.SessionID, store.TurnLoadReceipt{
		Trigger:    store.TurnLoadTriggerRequest,
		ToolCallID: tctx.Identity.ToolCallID,
		SurfaceID:  stateSurface,
		Standing:   marshalJSON(m.Ledger.Standing(tctx.Identity.SessionID)),
		Engine:     transcript.EngineLabel(outcome.Engine),
		StateJSON:  marshalJSON(state),
		Decisions:  marshalJSON(map[string]any{"request": outcome, "activation": result}),
		ElapsedMs:  elapsed.Milliseconds(),
		Abstained:  outcome.Abstained,
		Reason:     outcome.Reason,
	})
}

// LookupSkills ranks the session's skills against skills_read text and
// records the lookup as a receipt.
func (m *Service) LookupSkills(ctx context.Context, tctx tools.ToolContext, need string, roster []skills.Skill) turnload.LookupOutcome {
	catalog, err := turnload.LoadCatalog()
	if err != nil {
		return turnload.LookupSkills(ctx, nil, turnload.LookupSpec{}, need, roster)
	}
	sess := m.toolSession(ctx, tctx)
	started := time.Now()
	finishDeciding := m.beginDeciding(ctx, sess, tctx.Identity.SessionID, api.TurnLoadTriggerLookup, tctx.Identity.ToolCallID)
	outcome := turnload.LookupSkills(ctx, m.Decider(), catalog.Lookup, need, roster)
	finishDeciding()
	if sess == nil {
		return outcome
	}
	stateSurface := strings.TrimSpace(tctx.Turn.TurnSurfaceID)
	if stateSurface == "" {
		stateSurface = strings.TrimSpace(tctx.Identity.Agent)
	}
	m.recordTurnLoad(ctx, sess, tctx.Identity.SessionID, store.TurnLoadReceipt{
		Trigger:    store.TurnLoadTriggerLookup,
		ToolCallID: tctx.Identity.ToolCallID,
		SurfaceID:  stateSurface,
		Engine:     transcript.EngineLabel(outcome.Engine),
		StateJSON:  marshalJSON(map[string]any{"need": turnload.BoundUser(need, catalog.State.UserTextChars), "surface": stateSurface}),
		Decisions:  marshalJSON(map[string]any{"lookup": outcome}),
		ElapsedMs:  time.Since(started).Milliseconds(),
		Abstained:  outcome.Abstained,
		Reason:     outcome.Reason,
	})
	return outcome
}

// toolSession is the session a decision tool call runs in, or nil when the
// call has none the store knows.
func (m *Service) toolSession(ctx context.Context, tctx tools.ToolContext) *api.Session {
	if m == nil || m.store == nil || strings.TrimSpace(tctx.Identity.SessionID) == "" {
		return nil
	}
	sess, err := m.store.Get(ctx, tctx.Identity.SessionID)
	if err != nil {
		return nil
	}
	return sess
}

// beginDeciding opens a deciding lease while an available engine answers, so
// the composer shows the decision in progress. Without an engine nothing is
// published: an absent engine is a settings fact, never a turn phase.
func (m *Service) beginDeciding(ctx context.Context, sess *api.Session, sessionID string, trigger api.TurnLoadTrigger, toolCallID string) func() {
	if m == nil || m.events == nil || sess == nil || strings.TrimSpace(sessionID) == "" || !m.Decider().Available() {
		return func() {}
	}
	event := api.ActivityEvent{
		ActivityID:      uuid.NewString(),
		SessionID:       strings.TrimSpace(sessionID),
		Kind:            api.ActivityKindDeciding,
		Status:          api.ActivityStatusActive,
		StartedAt:       time.Now().UTC(),
		ToolCallID:      strings.TrimSpace(toolCallID),
		DecisionTrigger: trigger,
	}
	m.events.PublishActivity(ctx, strings.TrimSpace(sess.ProjectID), event.SessionID, event)
	var once sync.Once
	return func() {
		once.Do(func() {
			event.Status = api.ActivityStatusDone
			m.events.PublishActivity(context.WithoutCancel(ctx), strings.TrimSpace(sess.ProjectID), event.SessionID, event)
		})
	}
}

// recordTurnLoad writes one receipt under the session's current turn and
// publishes it to the transcript.
func (m *Service) recordTurnLoad(ctx context.Context, sess *api.Session, sessionID string, receipt store.TurnLoadReceipt) {
	if m == nil || m.store == nil || sess == nil {
		return
	}
	receipt.SessionID = sessionID
	if receipt.OpeningMessageID == "" {
		receipt.OpeningMessageID = m.Ledger.TurnMessageID(sessionID)
	}
	if receipt.Standing == "" {
		receipt.Standing = marshalJSON(m.Ledger.Standing(sessionID))
	}
	receipt.CreatedAt = time.Now().UTC()
	stored, err := m.store.PutTurnLoadReceipt(ctx, receipt)
	if err != nil {
		turnLoadLog.Warn("turn load receipt not recorded", "session_id", sessionID, "trigger", receipt.Trigger, "error", err)
		return
	}
	if m.events != nil {
		m.events.PublishTurnLoad(ctx, transcript.TurnLoadWire(stored))
	}
}

func marshalJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// recentToolNames lists distinct tool names the chat called, newest first.
func recentToolNames(history []api.Message, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for i := len(history) - 1; i >= 0 && len(out) < limit; i-- {
		msg := history[i]
		if msg.Role != api.MessageRoleAssistant {
			continue
		}
		for j := len(msg.ToolCalls) - 1; j >= 0 && len(out) < limit; j-- {
			name := strings.TrimSpace(msg.ToolCalls[j].Name)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// attachmentKinds names the content-part kinds and artifact presence on a prompt.
func attachmentKinds(in promptinput.Input) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(kind string) {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			return
		}
		if _, dup := seen[kind]; dup {
			return
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	if len(in.ArtifactIDs) > 0 {
		add("artifact")
	}
	for _, part := range in.ContentParts {
		kind := strings.TrimSpace(part.MediaType)
		if major, _, ok := strings.Cut(kind, "/"); ok {
			kind = major
		}
		if kind == "" {
			kind = strings.TrimSpace(part.Source)
		}
		add(kind)
	}
	return out
}
