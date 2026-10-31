package loading

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

var turnLoadLog = observability.LazyComponent("turn_load")

// SetLedger installs the ledger of loaded schemas shared with request_tools.
func (m *Service) SetLedger(ledger *turnload.Ledger) {
	if m != nil {
		m.Ledger = ledger
	}
}

// SetDecider installs the local decision model.
func (m *Service) SetDecider(d decide.Decider) {
	if m != nil {
		m.decider = d
	}
}

// LoadedTools returns the tools loaded beyond the surface floor for a session.
func (m *Service) LoadedTools(sessionID string) map[string]bool {
	if m == nil {
		return nil
	}
	return m.Ledger.Active(sessionID)
}

// OmittedUnits returns the instruction units the session's turn leaves out.
func (m *Service) OmittedUnits(sessionID string) map[string]bool {
	if m == nil {
		return nil
	}
	return m.Ledger.Omitted(sessionID)
}

func (m *Service) Decider() decide.Decider {
	if m == nil || m.decider == nil {
		return decide.Absent{}
	}
	return m.decider
}

// Decision is what one turn's load decision established.
type Decision struct {
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
func (m *Service) turnToolSets(ctx context.Context, sess *api.Session, profileID, surfaceID string, rootCount int) (floor, loadable []string, cards []turnload.ToolCard) {
	policy := m.policy()
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

// Begin runs the turn decision once per human request, or per worker
// leg's brief, when the governing workflow run declares turn decisions, and
// records it on the ledger. A host turn decides only for a request that has
// no decision yet; otherwise it keeps the request's loads. openingMessageID
// names the user message that opened the turn; a continuation passes none and
// the turn keeps the newest user message.
func (m *Service) Begin(
	ctx context.Context, sess *api.Session, sessionID string, in promptinput.Input, history []api.Message, surfaceID, profileID, openingMessageID string,
) *Decision {
	if m == nil || m.Ledger == nil || sess == nil {
		return nil
	}
	request := m.ResolvedRequest(ctx, sess)
	openingMessageID, text, ok := m.Request(ctx, in, history, openingMessageID, request)
	if !ok {
		return nil
	}
	if sess.IsWorkerChild() {
		text, ok = m.WorkerRequest(sess, in.WorkerJobID, openingMessageID, history)
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
	decision := &Decision{host: host, catalog: catalog, revision: units.Revision(), started: time.Now()}
	rootCount := m.Workspace.RootCount(ctx, sess)
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
	epoch := m.HistoryEpoch(ctx, sess)
	decision.state = turnload.State{
		Host:            string(host),
		User:            turnload.BoundUser(text, catalog.State.UserTextChars),
		RootCount:       rootCount,
		Posture:         string(sess.Posture),
		WorkersInFlight: m.implement(ctx, sess).WorkersInFlight,
		Surface:         stateSurface,
		RecentTools:     recentToolNames(history, catalog.State.RecentTools),
		Attachments:     attachmentKinds(in),
		Loaded:          neededForTurn(m.Ledger.Needed(sessionID), history, epoch),
	}
	d := m.Decider()
	finishDeciding := m.beginDeciding(ctx, sess, sessionID, api.TurnLoadTriggerTurn, "")
	decision.decision = turnload.Decide(ctx, d, catalog.Turn, decision.state, candidates)
	m.selectSkillPreload(ctx, sess, profileID, text, decision)
	finishDeciding()
	_, found := m.RecordedStanding(ctx, sessionID)
	decision.opening = turnload.TurnOpening{
		OpeningMessageID: openingMessageID,
		Request:          decision.state.User,
		Loadable:         loadable,
		Boundary:         m.Boundary(ctx, sess, found),
		HistoryEpoch:     epoch,
	}
	m.Ledger.BeginTurn(sessionID, decision.opening, decision.decision)
	return decision
}

// NewestUserMessageID is the id of the last message a person wrote.
func NewestUserMessageID(history []api.Message) string {
	msg, _ := NewestUserInstruction(history)
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

// Finish writes the turn's receipt.
func (m *Service) Finish(ctx context.Context, sess *api.Session, sessionID string, tctx tools.ToolContext, decision *Decision) {
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
func (d *Decision) receipt() store.TurnLoadReceipt {
	receipt := store.TurnLoadReceipt{
		Trigger:         store.TurnLoadTriggerTurn,
		SurfaceID:       d.state.Surface,
		Engine:          transcript.EngineLabel(d.decision.Engine, d.ranking.Engine),
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
func (m *Service) RecordedStanding(ctx context.Context, sessionID string) (turnload.Standing, bool) {
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
