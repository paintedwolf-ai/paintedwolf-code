package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// kickSweep renders kicks for recipients and checks each render's claims once
// per render and authority.
type kickSweep struct {
	s *Stock
	// envelopes lists the occurrence fixtures for one kick.
	envelopes func(reg *anchor.Registry, kick string) []map[string]any
	engine    *prompts.FileTemplateEngine
	cache     *hoststate.RenderCache
	report    func(string)
	rendered  int
	// inputs keys stock renders by the variables each kick reads.
	inputs *renderInputs
	// Temporary measurement.
	varsDur, renderDur, claimDur time.Duration
	renderMisses, claimRuns      int
}

func newKickSweep(s *Stock, cache *hoststate.RenderCache, report func(string)) *kickSweep {
	k := &kickSweep{s: s, envelopes: richKickEnvelopes, engine: s.Engine(), cache: cache, report: report}
	if !hasSiteOverlayFiles(s.Site) {
		k.inputs = stockRenderInputs(s)
	}
	return k
}

// checkClaimsCached runs check once per render key and authority key and
// reports the violations it returns.
func checkClaimsCached(cache *hoststate.RenderCache, renderKey, authKey string, report func(string), check func() []string) {
	var viols []string
	err := cache.CheckAuthority(renderKey, authKey, func() error {
		viols = check()
		if len(viols) > 0 {
			return fmt.Errorf("%s: %d claim violations", renderKey, len(viols))
		}
		return nil
	})
	for _, v := range viols {
		report(v)
	}
	if err != nil && len(viols) == 0 {
		report(err.Error())
	}
}

func (k *kickSweep) render(ctx context.Context, keyPrefix, kick string, reader prompts.Reader, data map[string]any) (string, string, error) {
	t0 := time.Now()
	fullVars, err := prompts.Vars(reader, stripReaderFactKeys(data))
	if err != nil {
		return "", "", err
	}
	keyVars := fullVars
	if k.inputs != nil {
		keyVars = k.inputs.project(prompts.KicksTemplatePrefix+kick+".md", fullVars)
	}
	rKey := renderCacheKeyForVars(keyPrefix+kick+":"+k.s.Site, k.s.Catalog.Revision, nil, keyVars)
	k.varsDur += time.Since(t0)
	t1 := time.Now()
	text, _, err := k.cache.RenderOrReuse(rKey, func() (string, error) {
		k.renderMisses++
		return k.engine.RenderKick(ctx, reader, kick, data)
	})
	k.renderDur += time.Since(t1)
	if err == nil {
		k.rendered++
	}
	return text, rKey, err
}

// distinctReaderIndexes picks the first recipient of each distinct reader
// fingerprint, which covers every distinct render input for a fixture.
func distinctReaderIndexes(n int, reader func(int) prompts.Reader) []int {
	seen := map[string]bool{}
	var out []int
	for i := range n {
		fp := reader(i).Fingerprint()
		if fp == "" || !seen[fp] {
			seen[fp] = true
			out = append(out, i)
		}
	}
	return out
}

// kickOccurrenceProducible reports whether the host could deliver data to the
// recipient: occurrence values naming tools or minted handles come from the
// recipient's own offered tools and mints.
func kickOccurrenceProducible(kick string, data map[string]any, reader prompts.Reader, offered []string) bool {
	if tool, ok := data["tool"].(string); ok && kick == "coordinator-progress-missing" && tool != "" && !slices.Contains(offered, tool) {
		return false
	}
	if named, ok := data["tools"].([]string); ok && slices.ContainsFunc(named, func(t string) bool { return !slices.Contains(offered, t) }) {
		return false
	}
	if data["advance_when_gate_met"] == "coordinator" && !slices.Contains(offered, "workflow_advance") {
		return false
	}
	mints := reader.Mints()
	if targets, ok := data["repair_targets"].([]map[string]any); ok && !mints["web"] {
		for _, t := range targets {
			if _, has := t["candidates"]; has {
				return false
			}
		}
	}
	if !mints["read"] {
		if _, has := data["evidence_digest"]; has {
			return false
		}
		if offenders, ok := data["offenders_sample"].([]string); ok && slices.ContainsFunc(offenders, isMintedHandle) {
			return false
		}
		if issues, ok := data["document_issues"].([]map[string]any); ok {
			for _, is := range issues {
				if offenders, ok := is["offenders"].([]string); ok && slices.ContainsFunc(offenders, isMintedHandle) {
					return false
				}
			}
		}
	}
	return true
}

func isMintedHandle(s string) bool { return strings.Contains(s, "#") }

// coordinatorKicks renders every non-phase coordinator kick: the canonical
// fixture across every coordinator authority, and each further fixture across
// one recipient per distinct reader.
func (k *kickSweep) coordinatorKicks(ctx context.Context, anchorReg *anchor.Registry, kicks []string, reps []coordRep) error {
	for _, kick := range kicks {
		if slices.ContainsFunc(phaseKickPrefixes, func(p string) bool { return strings.HasPrefix(kick, p) }) {
			continue
		}
		distinct := distinctReaderIndexes(len(reps), func(i int) prompts.Reader { return reps[i].auth.Reader })
		for envIdx, envBase := range k.envelopes(anchorReg, kick) {
			indexes := distinct
			if envIdx == 0 {
				indexes = allIndexes(len(reps))
			}
			for _, i := range indexes {
				if err := k.coordinatorKick(ctx, kick, envBase, reps[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (k *kickSweep) coordinatorKick(ctx context.Context, kick string, envBase map[string]any, cr coordRep) error {
	if kick == "coordinator-closeout" && !surface.SurfaceDeliversReport(cr.state.TurnProfile.SurfaceID) {
		return nil
	}
	if !kickOccurrenceProducible(kick, envBase, cr.auth.Reader, cr.auth.Offered) {
		return nil
	}
	if kick == "coordinator-process-finished" {
		// The settled handle stays live, so its control tools stay projected.
		_, held := envBase["held_call_handle"]
		auth, err := hoststate.FreezeTurnAuthorityWithResources(ctx, cr.state, toolcontract.ResourcePresence{CommandJobs: !held, HeldCalls: held})
		if err != nil {
			return err
		}
		cr.auth = auth
	}
	reader := cr.auth.Reader
	subj := toolvocabtest.SubjectSelfNow()
	var nextTurn *toolvocabtest.ActorAuthority
	data := maps.Clone(envBase)
	if kick == "coordinator-turn-closeout" || kick == "session-agent-enabled" {
		closeR, err := closeoutTurnReader(cr.state, false)
		if err != nil {
			return err
		}
		reader = closeR
		active := cr.auth.ActorAuthority()
		nextTurn = &active
		writes := cr.state.SessionMode != api.SessionModeAsk && cr.auth.Reader.Held()["command_mutation"]
		subj = toolvocabtest.SubjectSessionNextTurn(writes)
		data["session_command_mutation_capable"] = writes
	}
	if closeoutKicks[kick] && data["reason_text"] == nil {
		// Closeout always states its cause; the default cause is the iteration cap.
		data["reason_text"] = promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutIterationCap}.Text()
	}
	if kick == "coordinator-citation-grounding" {
		// The closeout retry sets it from the request's reach.
		data["can_recall"] = cr.auth.Reach.Status("recall") == toolreach.Callable
	}
	if err := prompts.MergeCoordinatorKickVarsForTools(data, reader); err != nil {
		return err
	}
	text, rKey, err := k.render(ctx, "kick:", kick, reader, data)
	if err != nil {
		return err
	}
	label := fmt.Sprintf("kick %s on %s (%s)", kick, cr.state.TurnProfile.SurfaceID, cr.state.ID)
	tc := time.Now()
	defer func() { k.claimDur += time.Since(tc) }()
	checkClaimsCached(k.cache, rKey, cr.auth.Digest+"|"+string(subj.Kind), k.report, func() []string {
		k.claimRuns++
		target := cr.auth
		target.Reader = reader
		if subj.Kind == toolvocabtest.SubjectKindSessionNextTurn {
			target.Metas = nil
			target.Schemas = map[string]map[string]any{}
			target.Offered = nil
		}
		return checkClaimsOnce(k.s, label, text, target, cr.state.SessionMode, cr.state.Posture, subj, nil, nextTurn, nil, true)
	})
	return nil
}

// workerKicks renders every worker kick for leg-holding workers, the canonical
// fixture across every worker authority and each further fixture across one
// worker per distinct reader.
func (k *kickSweep) workerKicks(ctx context.Context, anchorReg *anchor.Registry, kicks []string, allReps []workerRep) error {
	var reps []workerRep
	for _, wr := range allReps {
		if wr.auth.Reader.Held()["complete_leg"] {
			reps = append(reps, wr)
		}
	}
	distinct := distinctReaderIndexes(len(reps), func(i int) prompts.Reader { return reps[i].auth.Reader })
	for _, kick := range kicks {
		for envIdx, envBase := range k.envelopes(anchorReg, kick) {
			indexes := distinct
			if envIdx == 0 {
				indexes = allIndexes(len(reps))
			}
			for _, i := range indexes {
				if err := k.workerKick(ctx, kick, envBase, reps[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (k *kickSweep) workerKick(ctx context.Context, kick string, envBase map[string]any, wr workerRep) error {
	if !kickOccurrenceProducible(kick, envBase, wr.auth.Reader, wr.auth.Offered) {
		return nil
	}
	closeout := kick == "worker-closeout" || kick == "worker-cancel-closeout"
	reader := wr.auth.Reader
	subj := toolvocabtest.SubjectSelfNow()
	var followup *toolvocabtest.ActorAuthority
	data := maps.Clone(envBase)
	if closeout {
		closeR, err := workerCloseoutReader(wr.ws)
		if err != nil {
			return err
		}
		reader = closeR
		if kick == "worker-closeout" {
			active := wr.auth.ActorAuthority()
			followup = &active
			writes := wr.ws.ParentSessionMode != api.SessionModeAsk && wr.auth.Reader.Held()["command_mutation"]
			subj = toolvocabtest.SubjectFollowupLeg(writes)
			data["leg_command_mutation_capable"] = writes
		}
	}
	text, rKey, err := k.render(ctx, "wkick:", kick, reader, data)
	if err != nil {
		return err
	}
	label := fmt.Sprintf("kick %s for %s (%s)", kick, wr.ws.ToolProfile, wr.ws.ID)
	tc := time.Now()
	defer func() { k.claimDur += time.Since(tc) }()
	checkClaimsCached(k.cache, rKey, wr.auth.Digest+"|"+string(subj.Kind), k.report, func() []string {
		k.claimRuns++
		target := wr.auth
		target.Reader = reader
		if closeout {
			var closeMetas []tools.ToolMeta
			for _, m := range wr.auth.Metas {
				if m.Name == "complete_leg" {
					closeMetas = append(closeMetas, m)
				}
			}
			target.Metas = closeMetas
			target.Schemas = map[string]map[string]any{}
			if len(closeMetas) > 0 {
				target.Schemas["complete_leg"] = closeMetas[0].ArgsSchema
			}
			target.Offered = []string{"complete_leg"}
		}
		return checkClaimsOnce(k.s, label, text, target, wr.ws.ParentSessionMode, wr.ws.Posture, subj, followup, nil, nil, true)
	})
	return nil
}

func allIndexes(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// closeoutKicks names the kicks turnnudges.Closeout renders, true where it is
// the kick's only producer, so reason_text is always set.
var closeoutKicks = map[string]bool{
	"coordinator-closeout": true, "coordinator-turn-closeout": true, "worker-closeout": false, "worker-cancel-closeout": false,
}

// closeoutCauseEnvelopes is the data turnnudges.Closeout builds from each
// production turn-closeout cause.
func closeoutCauseEnvelopes(kick string) []map[string]any {
	if kick == "worker-cancel-closeout" {
		return []map[string]any{{"cancel_reason": "the person stopped this leg"}, {}}
	}
	if _, ok := closeoutKicks[kick]; !ok {
		return nil
	}
	causes := []promptloop.TurnCloseoutCause{
		{Reason: promptloop.TurnCloseoutIterationCap},
		{Reason: promptloop.TurnCloseoutSpendCeiling},
		{Reason: promptloop.TurnCloseoutStopped},
		{Reason: promptloop.TurnCloseoutLLMTimeout},
		{Reason: promptloop.TurnCloseoutBlockedLoop, BlockedTool: "command", BlockedCode: "PATH_DENIED", BlockedCount: promptloop.BlockedLoopRejectCap},
	}
	out := make([]map[string]any, 0, len(causes))
	for _, c := range causes {
		out = append(out, map[string]any{"reason_text": c.Text(), "llm_timeout": c.Reason == promptloop.TurnCloseoutLLMTimeout})
	}
	return out
}

func richKickEnvelopes(reg *anchor.Registry, kick string) []map[string]any {
	out := append(kickEnvelopeVariants(reg, kick), map[string]any{})
	out = append(out, closeoutCauseEnvelopes(kick)...)
	extraA := map[string]any{
		"reason_text":                  "closeout criteria open",
		"llm_timeout":                  true,
		"cancel_reason":                "user canceled leg",
		"progress_closure_armed":       true,
		"leg_id":                       "leg-alpha",
		"advance_when_gate_met":        "coordinator",
		"completed_ago":                "1m ago",
		"workers":                      []map[string]any{{"worker": "w1", "state": "working"}, {"worker": "", "state": "queued"}},
		"overlays":                     []map[string]any{{"worker": "w1", "id": "ov-1"}, {"worker": "", "id": "ov-2"}},
		"label":                        "@secondary",
		"command_external_changed":     true,
		"command_external_shown":       true,
		"offenders_omitted":            2,
		"resume_work_allowed":          true,
		"held_call_handle":             "held-1",
		"command_completion":           "cmd-1 exited 0",
		"command_refusal":              "SANDBOX_TRY_WRITE_ROOT on /opt",
		"run_report":                   true,
		"offending_keys":               []string{"status"},
		"allowed_top_level_keys":       "summary, findings",
		"document_issues":              []map[string]any{{"message": "bad citation", "offenders": []string{"read#99"}}},
		"offenders_sample":             []string{"read#99"},
		"pinned_body_chars":            512,
		"retained_document":            true,
		"changes":                      []map[string]any{{"kind": "added", "label": "a"}, {"kind": "removed", "label": "b"}, {"kind": "renamed", "old_label": "old-c", "label": "c"}},
		"changes_omitted":              1,
		"primary_changed":              true,
		"new_primary":                  map[string]any{"label": "a"},
		"after_count":                  2,
		"after":                        []map[string]any{{"label": "a", "is_active": true}, {"label": "c", "is_active": false}},
		"after_omitted_count":          1,
		"session_folder_changed":       map[string]any{"label": "a"},
		"active_root":                  map[string]any{"label": "a"},
		"pending_overlays":             1,
		"partial_workers":              0,
		"batch_phase":                  "integrate",
		"guidance":                     "Store secret via Den settings.",
		"tools":                        []string{"command", "recall"},
		"scanner_id":                   "semgrep",
		"scan_id":                      "scan-1",
		"introduced_count":             2,
		"introduced":                   []map[string]any{{"level": "high", "rule_id": "r1", "path": "a.go", "line": 10, "message": "issue"}, {"level": "low", "rule_id": "r2", "path": "b.go"}},
		"introduced_omitted_count":     1,
		"fixed_count":                  2,
		"fixed":                        []map[string]any{{"level": "high", "rule_id": "r1", "path": "c.go", "line": 4}, {"level": "low", "rule_id": "r3", "path": "d.go"}},
		"fixed_omitted_count":          1,
		"scan_categories":              "sast",
		"scan_findings_count":          3,
		"worker":                       "impl-1",
		"producer":                     "scout-1",
		"producer_state":               "closed",
		"notice_kind":                  "quiet",
		"round_ordinal":                2,
		"workflow_work_id":             "work-1",
		"in_flight":                    "tools",
		"tools_minutes":                3,
		"tokens":                       4200,
		"spend_under_cent":             true,
		"latest_message_seq":           4,
		"worker_result":                map[string]any{"result_handle": "res-1", "changed": []string{"a.go", "b.go"}, "changed_omitted": 1, "not_applied": []string{"c.go", "d.go"}},
		"worker_digest":                "2 files updated",
		"evidence_digest":              "[read#1] a.go:1",
		"last_worker_decision_request": map[string]any{"worker": "impl-1", "options": []string{"opt-a", "opt-b"}},
		"repair_targets": []map[string]any{
			{"claim": "c1", "web_link": true, "candidates": []string{"web#1"}, "use": "web#1", "worker_output": true},
			{"claim": "c2", "web_link": false},
		},
		"document_citation_targets": []map[string]any{{"section": "Risks", "claim": "ungrounded"}},
		"observed_handles_sample":   []string{"read#1"},
		"observed_handles_omitted":  1,
		"observed_paths_sample":     []string{"a.go"},
		"observed_paths_omitted":    1,
		"observed_urls_sample":      []string{"https://example.com"},
		"observed_urls_omitted":     1,
		"repair_observations":       true,
		"drafted_synthesis":         "Draft body.",
		"gate_obligations": []map[string]any{
			{"id": "plan_stub_valid", "purpose": "Plan sections", "required": []string{"Goal"}, "missing": []string{"Goal missing"}, "satisfy": []string{"Add Goal"}},
			{"id": "simple", "purpose": "Simple", "satisfy": []string{"Continue"}},
		},
	}
	extraB := map[string]any{
		"after_count":              0,
		"changes":                  []map[string]any{},
		"changes_omitted":          0,
		"primary_changed":          true,
		"new_primary":              map[string]any{"label": "b"},
		"pending_overlays":         2,
		"batch_phase":              "closed",
		"producer_state":           "rejected",
		"notice_kind":              "progress",
		"in_flight":                "queued",
		"spend_under_cent":         false,
		"estimated_spend_usd":      "0.04",
		"command_external_changed": true,
		"command_external_shown":   false,
		"advance_when_gate_met":    "host",
	}
	extraC := map[string]any{
		"after_count":            1,
		"after":                  []map[string]any{{"label": "b", "is_active": false}},
		"after_omitted_count":    0,
		"session_folder_changed": nil,
		"active_root":            nil,
		"pending_overlays":       0,
		"partial_workers":        2,
		"in_flight":              "model_call",
		"producer_state":         "pending",
	}
	extraD := map[string]any{
		"pending_overlays": 0,
		"partial_workers":  1,
		"in_flight":        "idle",
	}
	// Occurrence data for a recipient that mints no read or web handles.
	extraE := map[string]any{
		"document_issues":  []map[string]any{{"message": "bad citation", "offenders": []string{"ungrounded-ref"}}},
		"offenders_sample": []string{"ungrounded-ref"},
		"repair_targets":   []map[string]any{{"claim": "c1", "web_link": true}, {"claim": "c2", "web_link": false}},
	}
	return append(out, extraA, extraB, extraC, extraD, extraE)
}

func phaseKickVariants() []map[string]any {
	return []map[string]any{
		{
			"options_criterion":        "Minimize blast radius",
			"topology_output":          "3 recon findings",
			"fanout_plan":              "2 slices",
			"spawnable_reviewers":      []string{"skeptic"},
			"review_followup_attempts": 1,
			"rating_questions":         "severity: high|low",
			"review_verdict": map[string]any{
				"survey_claims":     map[string]any{"threat_model": "Untrusted HTTP input", "claims": "- claim-1: validated"},
				"survey_challenged": map[string]any{"challenges": "- claim-1: survives"},
			},
			"evidence_digest": "[read#1] main.go:1",
			"obligations":     map[string]any{"scan": map[string]any{"findings_count": 2, "error": "partial"}},
		},
		{
			"topology_output": "Recon summary only",
			"obligations":     map[string]any{"scan": map[string]any{"findings_count": 0, "error": ""}},
		},
		{},
	}
}
