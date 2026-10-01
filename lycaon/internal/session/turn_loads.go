package session

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

var turnLoadLog = observability.LazyComponent("turn_load")

// SetTurnLoads installs the ledger of loaded schemas shared with request_tools.
func (m *Manager) SetTurnLoads(ledger *turnload.Ledger) {
	if m != nil {
		m.turnLoads = ledger
	}
}

// SetDecider installs the local decision model.
func (m *Manager) SetDecider(d decide.Decider) {
	if m != nil {
		m.decider = d
	}
}

// LoadedTools returns the tools loaded beyond the surface floor for a session.
func (m *Manager) LoadedTools(sessionID string) map[string]bool {
	if m == nil {
		return nil
	}
	return m.turnLoads.Active(sessionID)
}

// OmittedUnits returns the instruction units the session's turn leaves out.
func (m *Manager) OmittedUnits(sessionID string) map[string]bool {
	if m == nil {
		return nil
	}
	return m.turnLoads.Omitted(sessionID)
}

func (m *Manager) turnDecider() decide.Decider {
	if m == nil || m.decider == nil {
		return decide.Absent{}
	}
	return m.decider
}

// turnDecision is what one turn's load decision established.
type turnDecision struct {
	host       promptunit.Host
	state      turnload.State
	floor      []string
	loadable   []string
	guides     []string
	decision   turnload.Decision
	ranking    turnload.Ranking
	skill      *skills.Skill
	skillScore float64
	preload    *turnload.SkillPreload
	catalog    turnload.Catalog
	revision   string
	started    time.Time
	opening    turnload.TurnOpening
}

// turnToolSets lists what a turn offers on every call and what it could load,
// with the cards the decision reads for the loadable part.
func (m *Manager) turnToolSets(ctx context.Context, sess *api.Session, profileID, surfaceID string, rootCount int) (floor, loadable []string, cards []turnload.ToolCard) {
	policy := m.PromptToolPolicy()
	if policy == nil {
		return nil, nil, nil
	}
	metas := policy.ListForPrompt(ctx, sess, profileID)
	byName := make(map[string]tools.ToolMeta, len(metas))
	for _, meta := range metas {
		byName[meta.Name] = meta
	}
	card := func(name string) turnload.ToolCard {
		return turnload.ToolCard{Name: name, Description: byName[name].Description}
	}
	if !guard.IsCoordinatorProfile(profileID) {
		for _, meta := range metas {
			if meta.Deferred {
				loadable = append(loadable, meta.Name)
				cards = append(cards, card(meta.Name))
			} else {
				floor = append(floor, meta.Name)
			}
		}
		return floor, loadable, cards
	}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, rootCount)
	if err != nil {
		return nil, nil, nil
	}
	floor = plan.ImmediateNames()
	for _, name := range plan.DeferredNames() {
		loadable = append(loadable, name)
		cards = append(cards, card(name))
	}
	if plan.Immediate("request_tools") {
		for _, meta := range metas {
			if meta.IsMCP() && !meta.AlwaysLoad {
				loadable = append(loadable, meta.Name)
				cards = append(cards, card(meta.Name))
			}
		}
	}
	return floor, loadable, cards
}

// beginTurnLoads runs the turn decision once per human request, or per worker
// leg's brief, when the governing workflow run declares turn decisions, and
// records it on the ledger. A host turn decides only for a request that has
// no decision yet; otherwise it keeps the request's loads. openingMessageID
// names the user message that opened the turn; a continuation passes none and
// the turn keeps the newest user message.
func (m *Manager) beginTurnLoads(
	ctx context.Context, sess *api.Session, sessionID string, in PromptInput, history []api.Message, surfaceID, profileID, openingMessageID string,
) *turnDecision {
	if m == nil || m.turnLoads == nil || sess == nil {
		return nil
	}
	request := m.resolvedWorkflowRequest(ctx, sess)
	openingMessageID, text, ok := m.turnRequest(ctx, in, history, openingMessageID, request)
	if !ok {
		return nil
	}
	if sess.IsWorkerChild() {
		text, ok = m.workerDecisionRequest(sess, in.WorkerJobID, openingMessageID, history)
		if !ok {
			m.beginUndecidedTurn(ctx, sess, history, surfaceID, profileID, openingMessageID)
			return nil
		}
	}
	catalog, err := turnload.LoadCatalog()
	if err != nil {
		turnLoadLog.Warn("decision catalog unavailable", "error", err)
		return nil
	}
	units, err := prompts.UnitCatalogFor(nil)
	if err != nil {
		turnLoadLog.Warn("unit catalog unavailable", "error", err)
		return nil
	}
	host := promptunit.HostWorker
	mode := ""
	if guard.IsCoordinatorProfile(profileID) {
		host = promptunit.HostCoordinator
		mode = surface.ExecutionModeFamily(surfaceID)
	}
	decision := &turnDecision{host: host, catalog: catalog, revision: units.Revision(), started: time.Now()}
	_, rootCount, _ := m.workspaceRootsForPrompt(ctx, sess)
	floor, loadable, cards := m.turnToolSets(ctx, sess, profileID, surfaceID, rootCount)
	decision.floor = floor
	decision.loadable = loadable
	guides := units.Candidates(host, mode, nameSet(floor), nameSet(loadable))
	for _, g := range guides {
		decision.guides = append(decision.guides, g.ID)
	}
	candidates := turnload.Candidates(turnload.ToolCandidates(cards), turnload.GuideCandidates(guides))
	stateSurface := surfaceID
	if host == promptunit.HostWorker {
		stateSurface = profileID
	}
	decision.state = turnload.State{
		Host:            string(host),
		User:            turnload.BoundUser(text, catalog.State.UserTextChars),
		RootCount:       rootCount,
		Posture:         string(sess.Posture),
		WorkersInFlight: m.BuildImplementSessionState(ctx, sess).WorkersInFlight,
		Surface:         stateSurface,
		RecentTools:     recentToolNames(history, catalog.State.RecentTools),
		Attachments:     attachmentKinds(in),
		Loaded:          m.turnLoads.Needed(sessionID),
	}
	d := m.turnDecider()
	finishDeciding := m.beginDeciding(ctx, sess, sessionID, api.TurnLoadTriggerTurn, "")
	decision.decision = turnload.Decide(ctx, d, catalog.Turn, decision.state, candidates)
	m.selectSkillPreload(ctx, sess, profileID, text, decision)
	finishDeciding()
	_, found := m.recordedStanding(ctx, sessionID)
	epoch := m.historyEpoch(ctx, sess)
	decision.opening = turnload.TurnOpening{
		OpeningMessageID: openingMessageID,
		Request:          decision.state.User,
		Loadable:         loadable,
		Boundary:         m.turnBoundary(ctx, sess, found),
		HistoryEpoch:     epoch,
	}
	m.turnLoads.BeginTurn(sessionID, decision.opening, decision.decision)
	return decision
}

// newestUserMessageID is the id of the last message a person wrote.
func newestUserMessageID(history []api.Message) string {
	msg, _ := newestUserInstruction(history)
	return strings.TrimSpace(msg.ID)
}

func nameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}

// finishTurnLoads writes the turn's receipt.
func (m *Manager) finishTurnLoads(ctx context.Context, sess *api.Session, sessionID string, tctx tools.ToolContext, decision *turnDecision) {
	if m == nil || decision == nil {
		return
	}
	m.renderSkillPreload(ctx, sessionID, tctx, decision)
	receipt := decision.receipt()
	m.recordTurnLoad(ctx, sess, sessionID, receipt)
	turnLoadLog.Info("turn load decided",
		"session_id", sessionID,
		"host", decision.host,
		"surface", decision.state.Surface,
		"tools", decision.decision.ToolIDs(),
		"omitted", decision.decision.OmittedIDs(),
		"cache", decision.opening.Boundary.Cache,
		"cold_reason", decision.opening.Boundary.Reason,
		"kind", decision.decision.Kind,
		"abstained", decision.decision.Abstained,
		"elapsed_ms", receipt.ElapsedMs)
}

// receipt is the turn's record: the state the engine read, what it answered,
// and the candidates the turn offered it, so a later label reads against the
// options the host actually had.
func (d *turnDecision) receipt() store.TurnLoadReceipt {
	receipt := store.TurnLoadReceipt{
		Trigger:         store.TurnLoadTriggerTurn,
		SurfaceID:       d.state.Surface,
		Engine:          engineLabel(d.decision.Engine, d.ranking.Engine),
		CatalogRevision: d.revision,
		StateJSON:       marshalJSON(d.state),
		Decisions: marshalJSON(map[string]any{
			"turn": d.decision, "skills": d.ranking, "preloaded_skill": preloadIdentity(d.preload), "floor": d.floor, "boundary": d.opening.Boundary,
			"candidates": map[string][]string{"loadable": append([]string{}, d.loadable...), "guides": append([]string{}, d.guides...)},
		}),
		ElapsedMs: time.Since(d.started).Milliseconds(),
		Abstained: d.decision.Abstained && d.ranking.Engine.Name == "",
		Reason:    d.decision.Reason,
	}
	if !receipt.Abstained {
		receipt.Reason = ""
	}
	return receipt
}

// recordedStanding returns the standing surface the session's latest
// receipt recorded; found is false before the chat's first receipt.
func (m *Manager) recordedStanding(ctx context.Context, sessionID string) (turnload.Standing, bool) {
	if m == nil || m.store == nil {
		return turnload.Standing{}, false
	}
	receipt, ok, err := m.store.LatestTurnLoadReceipt(ctx, sessionID)
	if err != nil {
		turnLoadLog.Warn("recorded standing surface unreadable", "session_id", sessionID, "error", err)
		return turnload.Standing{}, false
	}
	if !ok {
		return turnload.Standing{}, false
	}
	var standing turnload.Standing
	if json.Unmarshal([]byte(receipt.Standing), &standing) != nil {
		return turnload.Standing{}, true
	}
	return standing, true
}

// ResolveToolRequest ranks requestable schemas; the handler records the final activation.
func (m *Manager) ResolveToolRequest(ctx context.Context, tctx tools.ToolContext, need string, cards []turnload.ToolCard) turnload.RequestOutcome {
	catalog, err := turnload.LoadCatalog()
	if err != nil {
		return turnload.RequestOutcome{Need: need, Exact: turnload.ExactNames(need, cards), Abstained: true, Reason: "decision catalog unavailable", Failure: turnload.RankingUnavailable}
	}
	sess := m.toolSession(ctx, tctx)
	finishDeciding := m.beginDeciding(ctx, sess, tctx.SessionID, api.TurnLoadTriggerRequest, tctx.ToolCallID)
	outcome := turnload.ResolveRequest(ctx, m.turnDecider(), catalog.Request, need, cards)
	finishDeciding()
	return outcome
}

// RecordToolRequest persists the standing surface after filtering and activation,
// so receipts and restart recovery agree with the next model call.
func (m *Manager) RecordToolRequest(ctx context.Context, tctx tools.ToolContext, outcome turnload.RequestOutcome, result turnload.RequestToolsResult, elapsed time.Duration) {
	sess := m.toolSession(ctx, tctx)
	if sess == nil {
		return
	}
	userChars := 900
	if catalog, err := turnload.LoadCatalog(); err == nil {
		userChars = catalog.State.UserTextChars
	}
	host := promptunit.HostWorker
	if guard.IsCoordinatorProfile(tctx.Agent) {
		host = promptunit.HostCoordinator
	}
	stateSurface := strings.TrimSpace(tctx.TurnSurfaceID)
	if stateSurface == "" {
		stateSurface = strings.TrimSpace(tctx.Agent)
	}
	state := turnload.State{Host: string(host), User: turnload.BoundUser(outcome.Need, userChars), Surface: stateSurface}
	m.recordTurnLoad(ctx, sess, tctx.SessionID, store.TurnLoadReceipt{
		Trigger:    store.TurnLoadTriggerRequest,
		ToolCallID: tctx.ToolCallID,
		SurfaceID:  stateSurface,
		Standing:   marshalJSON(m.turnLoads.Standing(tctx.SessionID)),
		Engine:     engineLabel(outcome.Engine),
		StateJSON:  marshalJSON(state),
		Decisions:  marshalJSON(map[string]any{"request": outcome, "activation": result}),
		ElapsedMs:  elapsed.Milliseconds(),
		Abstained:  outcome.Abstained,
		Reason:     outcome.Reason,
	})
}

// LookupSkills ranks the session's skills against skills_read text and
// records the lookup as a receipt.
func (m *Manager) LookupSkills(ctx context.Context, tctx tools.ToolContext, need string, roster []skills.Skill) turnload.LookupOutcome {
	catalog, err := turnload.LoadCatalog()
	if err != nil {
		return turnload.LookupSkills(ctx, nil, turnload.LookupSpec{}, need, roster)
	}
	sess := m.toolSession(ctx, tctx)
	started := time.Now()
	finishDeciding := m.beginDeciding(ctx, sess, tctx.SessionID, api.TurnLoadTriggerLookup, tctx.ToolCallID)
	outcome := turnload.LookupSkills(ctx, m.turnDecider(), catalog.Lookup, need, roster)
	finishDeciding()
	if sess == nil {
		return outcome
	}
	stateSurface := strings.TrimSpace(tctx.TurnSurfaceID)
	if stateSurface == "" {
		stateSurface = strings.TrimSpace(tctx.Agent)
	}
	m.recordTurnLoad(ctx, sess, tctx.SessionID, store.TurnLoadReceipt{
		Trigger:    store.TurnLoadTriggerLookup,
		ToolCallID: tctx.ToolCallID,
		SurfaceID:  stateSurface,
		Engine:     engineLabel(outcome.Engine),
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
func (m *Manager) toolSession(ctx context.Context, tctx tools.ToolContext) *api.Session {
	if m == nil || m.store == nil || strings.TrimSpace(tctx.SessionID) == "" {
		return nil
	}
	sess, err := m.store.Get(ctx, tctx.SessionID)
	if err != nil {
		return nil
	}
	return sess
}

// beginDeciding opens a deciding lease while an available engine answers, so
// the composer shows the decision in progress. Without an engine nothing is
// published: an absent engine is a settings fact, never a turn phase.
func (m *Manager) beginDeciding(ctx context.Context, sess *api.Session, sessionID string, trigger api.TurnLoadTrigger, toolCallID string) func() {
	if m == nil || m.events == nil || sess == nil || strings.TrimSpace(sessionID) == "" || !m.turnDecider().Available() {
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
	m.events.PublishActivity(ctx, sessionProjectKey(sess), event.SessionID, event)
	var once sync.Once
	return func() {
		once.Do(func() {
			event.Status = api.ActivityStatusDone
			m.events.PublishActivity(context.WithoutCancel(ctx), sessionProjectKey(sess), event.SessionID, event)
		})
	}
}

// recordTurnLoad writes one receipt under the session's current turn and
// publishes it to the transcript.
func (m *Manager) recordTurnLoad(ctx context.Context, sess *api.Session, sessionID string, receipt store.TurnLoadReceipt) {
	if m == nil || m.store == nil || sess == nil {
		return
	}
	receipt.SessionID = sessionID
	if receipt.OpeningMessageID == "" {
		receipt.OpeningMessageID = m.turnLoads.TurnMessageID(sessionID)
	}
	if receipt.Standing == "" {
		receipt.Standing = marshalJSON(m.turnLoads.Standing(sessionID))
	}
	receipt.CreatedAt = time.Now().UTC()
	stored, err := m.store.PutTurnLoadReceipt(ctx, receipt)
	if err != nil {
		turnLoadLog.Warn("turn load receipt not recorded", "session_id", sessionID, "trigger", receipt.Trigger, "error", err)
		return
	}
	if m.events != nil {
		m.events.PublishTurnLoad(ctx, TurnLoadWire(stored))
	}
}

// TurnLoadsForPage projects the receipts of the turns a transcript page shows.
func (m *Manager) TurnLoadsForPage(ctx context.Context, page api.SessionTranscriptPage) ([]api.TurnLoad, error) {
	out := []api.TurnLoad{}
	if m == nil || m.store == nil || len(page.Messages) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(page.Messages))
	for _, msg := range page.Messages {
		if msg.Role == api.MessageRoleUser {
			ids = append(ids, msg.ID)
		}
	}
	receipts, err := m.store.ListTurnLoadReceiptsForTurns(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range receipts {
		out = append(out, TurnLoadWire(r))
	}
	return out, nil
}

func marshalJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func engineLabel(engines ...decide.Engine) string {
	for _, e := range engines {
		if e.Name != "" {
			label := e.Name + "/" + e.Model
			if e.Head != "" {
				label += "#" + e.Head
			}
			return label
		}
	}
	return ""
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
func attachmentKinds(in PromptInput) []string {
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
