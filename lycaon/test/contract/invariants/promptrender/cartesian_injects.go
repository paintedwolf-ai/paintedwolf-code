package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

type coordRep struct {
	state hoststate.CoordinatorState
	auth  hoststate.TurnAuthority
}

type workerRep struct {
	ws   hoststate.WorkerState
	auth hoststate.TurnAuthority
}

func runKicksAndInjectsMatrix(
	ctx context.Context,
	s *Stock,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	cReps, err := cachedCoordinatorReps()
	if err != nil {
		return err
	}
	wReps, err := cachedWorkerReps()
	if err != nil {
		return err
	}
	resolver, err := newNamedWorkerResolver(ctx, s)
	if err != nil {
		return err
	}

	if err := runAllKicksAndNudges(ctx, s, cReps, wReps, cache, sum); err != nil {
		return err
	}
	if err := runCoordinatorInjectRenders(ctx, s, cReps, resolver, sum); err != nil {
		return err
	}
	if err := runWorkerInjectRenders(ctx, s, wReps, cache, sum); err != nil {
		return err
	}
	if err := runGuidanceAndUtilityRenders(ctx, s, cReps, wReps, sum); err != nil {
		return err
	}
	return runGateFeedbackRenders(ctx, s, cReps, sum)
}

func runAllKicksAndNudges(
	ctx context.Context,
	s *Stock,
	cReps []coordRep,
	wReps []workerRep,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	engine := s.Engine()
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		return err
	}
	wfReg, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return err
	}

	var coordKicks, workerKicks []string
	for _, b := range anchorReg.AllInform() {
		if b == nil || !b.IsInform() {
			continue
		}
		switch {
		case strings.HasPrefix(b.Render, "coordinator-") && !slices.Contains(coordKicks, b.Render):
			coordKicks = append(coordKicks, b.Render)
		case strings.HasPrefix(b.Render, "worker-") && !slices.Contains(workerKicks, b.Render):
			workerKicks = append(workerKicks, b.Render)
		case b.Render == "session-agent-enabled" && !slices.Contains(coordKicks, b.Render):
			coordKicks = append(coordKicks, b.Render)
		}
	}
	slices.Sort(coordKicks)
	slices.Sort(workerKicks)

	for _, kick := range coordKicks {
		if err := renderCoordinatorKickAcrossReps(ctx, s, engine, anchorReg, kick, cReps, cache, sum); err != nil {
			return err
		}
	}
	for _, kick := range workerKicks {
		if err := renderWorkerKickAcrossReps(ctx, s, engine, anchorReg, kick, wReps, cache, sum); err != nil {
			return err
		}
	}
	return renderWorkflowPhaseKicks(ctx, s, engine, wfReg, cReps, cache, sum)
}

func renderCoordinatorKickAcrossReps(
	ctx context.Context,
	s *Stock,
	engine *prompts.FileTemplateEngine,
	anchorReg *anchor.Registry,
	kick string,
	cReps []coordRep,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	if slices.ContainsFunc(phaseKickPrefixes, func(p string) bool { return strings.HasPrefix(kick, p) }) {
		return nil
	}
	envs := richKickEnvelopes(anchorReg, kick)
	for envIdx, envBase := range envs {
		limit := len(cReps)
		if envIdx > 0 && limit > 10 {
			limit = 10
		}
		for i := 0; i < limit; i++ {
			cr := cReps[i]
			if kick == "coordinator-closeout" && !surface.SurfaceDeliversReport(cr.state.TurnProfile.SurfaceID) {
				continue
			}
			reader := cr.auth.Reader
			subj := toolvocabtest.SubjectSelfNow()
			var nextTurn *toolvocabtest.ActorAuthority
			data := maps.Clone(envBase)
			alignKickOccurrenceToAuthority(kick, data, cr.auth.Reader, cr.auth.Offered)
			if kick == "coordinator-turn-closeout" || kick == "session-agent-enabled" {
				closeR, err := closeoutTurnReader(cr.state, false)
				if err != nil {
					return err
				}
				reader = closeR
				active := cr.auth.ActorAuthority()
				nextTurn = &active
				subj = toolvocabtest.SubjectSessionNextTurn()
				data["session_command_mutation_capable"] = cr.state.SessionMode != api.SessionModeAsk && cr.auth.Reader.Held()["command_mutation"]
			}
			prompts.MergeCoordinatorKickVarsForTools(data, reader)
			fullVars, _ := prompts.Vars(reader, stripReaderFactKeys(data))
			rKey := renderCacheKeyForVars("mkick:"+kick+":"+s.Site, s.Catalog.Revision, nil, fullVars)
			text, _, err := cache.RenderOrReuse(rKey, func() (string, error) {
				return engine.RenderKick(ctx, reader, kick, data)
			})
			if err != nil {
				return err
			}
			sum.KickAndNudgeCount++
			label := fmt.Sprintf("kick %s (%s)", kick, cr.state.ID)
			checkKey := cr.auth.Digest + "|" + string(subj.Kind)
			_ = cache.CheckAuthority(rKey, checkKey, func() error {
				targetAuth := cr.auth
				targetAuth.Reader = reader
				if subj.Kind == toolvocabtest.SubjectKindSessionNextTurn {
					targetAuth.Metas = nil
					targetAuth.Schemas = map[string]map[string]any{}
					targetAuth.Offered = nil
				}
				viols, cErr := checkClaimsOnce(s, label, text, targetAuth, cr.state.SessionMode, cr.state.Posture, subj, nil, nextTurn, nil, true)
				sum.Violations = append(sum.Violations, viols...)
				return cErr
			})
		}
	}
	return nil
}

func renderWorkerKickAcrossReps(
	ctx context.Context,
	s *Stock,
	engine *prompts.FileTemplateEngine,
	anchorReg *anchor.Registry,
	kick string,
	wReps []workerRep,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	envs := richKickEnvelopes(anchorReg, kick)
	for envIdx, envBase := range envs {
		limit := len(wReps)
		if envIdx > 0 && limit > 8 {
			limit = 8
		}
		for i := 0; i < limit; i++ {
			wr := wReps[i]
			if !wr.auth.Reader.Held()["complete_leg"] {
				continue
			}
			reader := wr.auth.Reader
			subj := toolvocabtest.SubjectSelfNow()
			var followup *toolvocabtest.ActorAuthority
			data := maps.Clone(envBase)
			if kick == "worker-closeout" || kick == "worker-cancel-closeout" {
				closeR, err := workerCloseoutReader(wr.ws)
				if err != nil {
					return err
				}
				reader = closeR
				if kick == "worker-closeout" {
					active := wr.auth.ActorAuthority()
					followup = &active
					subj = toolvocabtest.SubjectFollowupLeg()
					data["leg_command_mutation_capable"] = wr.ws.ParentSessionMode != api.SessionModeAsk && wr.auth.Reader.Held()["command_mutation"]
				}
			}
			fullVars, _ := prompts.Vars(reader, stripReaderFactKeys(data))
			rKey := renderCacheKeyForVars("mwkick:"+kick+":"+s.Site, s.Catalog.Revision, nil, fullVars)
			text, _, err := cache.RenderOrReuse(rKey, func() (string, error) {
				return engine.RenderKick(ctx, reader, kick, data)
			})
			if err != nil {
				return err
			}
			sum.KickAndNudgeCount++
			label := fmt.Sprintf("worker kick %s (%s)", kick, wr.ws.ID)
			checkKey := wr.auth.Digest + "|" + string(subj.Kind)
			_ = cache.CheckAuthority(rKey, checkKey, func() error {
				targetAuth := wr.auth
				targetAuth.Reader = reader
				if kick == "worker-closeout" || kick == "worker-cancel-closeout" {
					var closeMetas []tools.ToolMeta
					for _, m := range wr.auth.Metas {
						if m.Name == "complete_leg" {
							closeMetas = append(closeMetas, m)
						}
					}
					targetAuth.Metas = closeMetas
					targetAuth.Schemas = map[string]map[string]any{}
					if len(closeMetas) > 0 {
						targetAuth.Schemas["complete_leg"] = closeMetas[0].ArgsSchema
					}
					targetAuth.Offered = []string{"complete_leg"}
				}
				viols, cErr := checkClaimsOnce(s, label, text, targetAuth, wr.ws.ParentSessionMode, wr.ws.Posture, subj, followup, nil, nil, true)
				sum.Violations = append(sum.Violations, viols...)
				return cErr
			})
		}
	}
	return nil
}

func renderWorkflowPhaseKicks(
	ctx context.Context,
	s *Stock,
	engine *prompts.FileTemplateEngine,
	wfReg *workflowdef.Registry,
	cReps []coordRep,
	cache *hoststate.RenderCache,
	sum *CartesianSummary,
) error {
	bySurface := map[string][]coordRep{}
	for _, cr := range cReps {
		if cr.state.PackOverlayID == "stock" {
			bySurface[cr.state.TurnProfile.SurfaceID] = append(bySurface[cr.state.TurnProfile.SurfaceID], cr)
		}
	}
	phaseVariants := phaseKickVariants()
	for _, wfKey := range sortedKeys(wfReg.All()) {
		m := wfReg.All()[wfKey]
		surfaces := map[string]string{}
		for _, p := range m.PhaseDefs {
			surfaces[p.ID] = p.CoordinatorSurface
		}
		for _, in := range m.Injects {
			if in.On != "phase.entered" || in.Selector.Phase == nil {
				continue
			}
			surfID := surfaces[*in.Selector.Phase]
			for _, cr := range bySurface[surfID] {
				for vIdx, pv := range phaseVariants {
					if vIdx > 0 && cr.state.Posture != api.SessionPostureBuild {
						continue
					}
					data := maps.Clone(pv)
					prompts.MergeCoordinatorKickVarsForTools(data, cr.auth.Reader)
					fullVars, _ := prompts.Vars(cr.auth.Reader, stripReaderFactKeys(data))
					rKey := renderCacheKeyForVars("mphase:"+in.Render+":"+s.Site, s.Catalog.Revision, nil, fullVars)
					text, _, err := cache.RenderOrReuse(rKey, func() (string, error) {
						return engine.RenderKick(ctx, cr.auth.Reader, in.Render, data)
					})
					if err != nil {
						return err
					}
					sum.KickAndNudgeCount++
					label := fmt.Sprintf("phase kick %s on %s (%s)", in.Render, surfID, cr.state.ID)
					_ = cache.CheckAuthority(rKey, cr.auth.Digest, func() error {
						viols, cErr := checkClaimsOnce(s, label, text, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
						sum.Violations = append(sum.Violations, viols...)
						return cErr
					})
				}
			}
		}
	}
	return nil
}

func richKickEnvelopes(reg *anchor.Registry, kick string) []map[string]any {
	out := append(kickEnvelopeVariants(reg, kick), map[string]any{})
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
	return append(out, extraA, extraB, extraC, extraD)
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

func runCoordinatorInjectRenders(
	ctx context.Context,
	s *Stock,
	cReps []coordRep,
	resolver *namedWorkerResolver,
	sum *CartesianSummary,
) error {
	engine := s.Engine()
	injRenderer := prompts.NewInjectRenderer(engine)
	now := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
	jobsBoth := []bgprocess.JobSnapshot{
		{Handle: "cmd-run", Mode: bgprocess.JobModeAwaited, OriginTool: "command", StartedAt: now.Add(-30 * time.Second), Timeout: 2 * time.Minute, Stages: []hostcmd.StageResult{{Command: "git status"}}},
		{Handle: "cmd-exit", Mode: bgprocess.JobModeBackground, OriginTool: "verify", StartedAt: now.Add(-2 * time.Minute), Exited: true, ExitCode: 1, Refusals: 2},
		{Handle: "cmd-clean", Mode: bgprocess.JobModeBackground, OriginTool: "command", StartedAt: now.Add(-1 * time.Minute), Exited: true, ExitCode: 0, Refusals: 0},
	}
	heldRunning := []heldcall.Running{{Handle: "held-1", Tool: "ask_user", Elapsed: 15 * time.Second}}

	for idx, cr := range cReps {
		resolveNamed := resolver.forCoordinatorState(cr.state)
		waitSchema := cr.auth.Schemas["wait"]

		// 1. command-jobs (running+exited+held and exited-only)
		for _, withRunning := range []bool{true, false} {
			jobs := jobsBoth
			var held []heldcall.Running
			if withRunning {
				held = heldRunning
			} else if idx > 4 {
				continue
			} else {
				jobs = jobsBoth[1:]
			}
			cmdBlock := inject.RenderCommandJobsBlock(ctx, injRenderer, cr.auth.Reader, waitSchema, "sess-inj", jobs, held, now, "coordinator")
			if cmdBlock != "" {
				sum.InjectAndGuidanceCount++
				viols, _ := checkClaimsOnce(s, "inject/command-jobs ("+cr.state.ID+")", cmdBlock, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}

		// 2. turn-state (rich workers + minimal)
		tsInputs := []inject.TurnStateInjectInput{
			{
				Reader: cr.auth.Reader, ProgressSnapshot: "- [ ] step 1", ProgressMissing: false, VerifyRequired: true,
				WorkersOmitted: 2,
				Workers: []map[string]any{
					{"name": "w-deliv", "agent": "implementer", "scope_mode": "write", "workspace_state": "overlay", "state": "delivered", "about": "Fix bug", "result_handle": "res-1", "shelve_reason": "superseded", "changed": []string{"a.go", "b.go"}, "changed_omitted": 1, "read": []string{"a.go", "c.go"}, "read_omitted": 1},
					{"name": "w-ask", "agent": "repo-researcher", "state": "asking", "question": "Use v2?"},
					{"name": "w-fail", "agent": "implementer", "state": "failed", "reason": "build error"},
					{"name": "w-idle", "agent": "implementer", "state": "idle", "parked_reason": "awaiting input"},
				},
			},
			{
				Reader: cr.auth.Reader, ProgressMissing: true, VerifyRequired: false, WorkersOmitted: 1,
				Workers: nil,
			},
		}
		for tIdx, tsIn := range tsInputs {
			if tIdx > 0 && idx > 6 {
				continue
			}
			tsBlock, err := inject.RenderTurnStateBlock(ctx, injRenderer, "sess-ts", tsIn)
			if err != nil {
				return err
			}
			sum.InjectAndGuidanceCount++
			if tsBlock != "" {
				viols, _ := checkClaimsOnce(s, "inject/turn-state ("+cr.state.ID+")", tsBlock, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}

		// 3. board-orientation, source-changes, implement-spawn, transition
		// (across first 16 distinct authorities + all 0/2-root authorities).
		if idx < 16 || cr.state.RootCount != 1 {
			if err := renderRemainingCoordInjectsForRep(ctx, s, injRenderer, cr, resolveNamed, now, idx, sum); err != nil {
				return err
			}
		}
	}

	if err := renderActiveWorkflowInjectVariants(ctx, s, injRenderer, cReps, resolver, sum); err != nil {
		return err
	}
	return renderStructuralInjectVariants(ctx, s, injRenderer, cReps[0], sum)
}

func renderRemainingCoordInjectsForRep(
	ctx context.Context,
	s *Stock,
	injRenderer *prompts.InjectRenderer,
	cr coordRep,
	resolveNamed func(string) (toolvocabtest.ActorAuthority, error),
	now time.Time,
	idx int,
	sum *CartesianSummary,
) error {
	namedWorkers := make([]api.BoardWorker, 9)
	for w := range namedWorkers {
		namedWorkers[w] = api.BoardWorker{
			Name: fmt.Sprintf("w%d", w+1), AgentType: "implementer", State: "working",
		}
	}
	snaps := []struct {
		snap   api.BoardSnapshot
		legend bool
	}{
		{
			legend: true,
			snap: api.BoardSnapshot{
				Repo:    api.RepoBrief{Languages: []string{"Go"}, FileCount: 42, Layout: api.RepoLayout{Files: []string{"cmd/main.go"}}},
				Workers: &api.BoardWorkersSlice{"workers": namedWorkers},
				OrientationRoots: []api.BoardOrientationRoot{
					{Label: "primary", IsPrimary: true, Brief: api.RepoBrief{Languages: []string{"Go"}, FileCount: 20, Layout: api.RepoLayout{Files: []string{"cmd/main.go"}}}, Truncated: true},
					{Label: "secondary", Brief: api.RepoBrief{Languages: []string{"Markdown"}, FileCount: 5, Layout: api.RepoLayout{Files: []string{"README.md"}}}, Truncated: false},
					{Label: "third", Brief: api.RepoBrief{Languages: []string{"YAML"}, FileCount: 2}},
				},
			},
		},
		{
			legend: false,
			snap:   api.BoardSnapshot{Repo: api.RepoBrief{Languages: []string{"Go"}, FileCount: 10}},
		},
	}
	for sIdx, sb := range snaps {
		if sIdx > 0 && idx > 4 {
			continue
		}
		bText, err := inject.RenderBoardOrientationInject(ctx, injRenderer, cr.auth.Reader, "sess-b", sb.snap, packboard.InjectScopeFull, false, sb.legend, now)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		if bText != "" {
			viols, _ := checkClaimsOnce(s, "inject/board-orientation ("+cr.state.ID+")", bText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	// Exercise inventory_at branch in board-orientation.md directly via RenderWithReader.
	if idx == 0 {
		invText, err := injRenderer.RenderWithReader(ctx, cr.auth.Reader, "board-orientation", map[string]any{
			"pack_sentinel": inject.BoardOrientationInjectSentinel,
			"inventory_at":  "14:00 UTC",
			"lines":         []map[string]any{{"text": "Inventory fresh."}},
		})
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		viols, _ := checkClaimsOnce(s, "inject/board-orientation-inv", invText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
		sum.Violations = append(sum.Violations, viols...)
	}

	// source-changes (rich + minimal, only when folders are attached)
	if cr.state.RootCount > 0 {
		fileHandle := "source_change#1"
		var gitLines []inject.SourceGitLine
		if cr.state.HasGit || cr.state.HasAnyGitRoot {
			gitLines = []inject.SourceGitLine{
				{Handle: "source_change#2", Root: "@secondary", Kind: "checkout", FromRef: "main", ToRef: "feat", FromCommit: "111", ToCommit: "222", Detail: "switch", At: "14:00 UTC"},
				{Kind: "commit", FromRef: "feat", ToRef: "feat", ToCommit: "333", At: "14:01 UTC"},
				{Kind: "reset", At: "14:02 UTC"},
			}
		}
		scBriefs := []inject.SourceChangeBrief{
			{
				Truncated: true, OwnTruncated: true, OtherFiles: 5, OtherEffects: 9,
				Files: []inject.SourceChangeFile{
					{Handle: fileHandle, Path: "a.go", Actor: "agent", Detail: "w1", Op: "modify", At: "14:00 UTC", Effects: 3, WorkingSet: true},
					{Path: "b.go", Actor: "user", Op: "create", At: "14:01 UTC", Effects: 1, WorkingSet: false, DuringYourCommand: true},
				},
				Git: gitLines,
			},
			{
				OwnTruncated: true,
				Files:        []inject.SourceChangeFile{{Path: "only.go", Actor: "user", Op: "modify", At: "14:00 UTC", Effects: 1, WorkingSet: true}},
			},
		}
		for bIdx, br := range scBriefs {
			if bIdx > 0 && idx > 8 {
				continue
			}
			scText := inject.RenderSourceChangesBlock(ctx, injRenderer, cr.auth.Reader, "sess-sc", br)
			if scText != "" {
				sum.InjectAndGuidanceCount++
				viols, _ := checkClaimsOnce(s, "inject/source-changes ("+cr.state.ID+")", scText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}
	}

	// implement-spawn (repoKnownEmpty false & true)
	for _, emptyRepo := range []bool{false, true} {
		if emptyRepo && idx > 6 {
			continue
		}
		spText, err := inject.RenderImplementSpawnInject(ctx, injRenderer, "sess-sp", cr.state.SpawnAllowlist, spawn.MaxInFlightTaskWorkers, cr.auth.Reader, emptyRepo)
		if err != nil {
			return err
		}
		if spText != "" {
			sum.InjectAndGuidanceCount++
			viols, _ := checkClaimsOnce(s, "inject/implement-spawn ("+cr.state.ID+")", spText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}

	// transition.md
	for _, entered := range []string{"investigate", "orchestrate", "wrapup"} {
		if idx > 4 {
			break
		}
		trText, err := injRenderer.RenderWithReader(ctx, cr.auth.Reader, "transition", map[string]any{
			"execution_mode_entered": entered,
			"execution_mode_left":    "investigate",
		})
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		if strings.TrimSpace(trText) != "" {
			viols, _ := checkClaimsOnce(s, "inject/transition ("+cr.state.ID+")", trText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}
