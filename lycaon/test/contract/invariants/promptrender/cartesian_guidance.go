package promptrender

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func runGuidanceAndUtilityRenders(
	ctx context.Context,
	s *Stock,
	cReps []coordRep,
	wReps []workerRep,
	sum *CartesianSummary,
) error {
	engine := s.Engine()
	gr := prompts.NewGuidanceRenderer(engine)
	ur := prompts.NewUtilityPromptRenderer(engine)
	zeroAuth := zeroToolAuthority(cReps[0].auth)

	// RenderWithReach templates across Coordinator and Worker Reach projections and narrowed recall schema.
	reachAuths, err := collectDistinctReachAuthorities(s, cReps, wReps)
	if err != nil {
		return err
	}
	for _, ra := range reachAuths {
		if err := renderReachBoundGuidance(ctx, s, gr, ra.label, ra.auth, sum); err != nil {
			return err
		}
	}

	// RenderWithReader templates: survey/*-specifics and editor/*, once per
	// distinct reader.
	for _, idx := range distinctReaderIndexes(len(cReps), func(i int) prompts.Reader { return cReps[i].auth.Reader }) {
		cr := cReps[idx]
		for _, ref := range []string{"survey/grep-specifics", "survey/find-specifics", "survey/read-specifics", "survey/jq-specifics"} {
			if !cr.auth.Reader.Held()["command"] {
				continue
			}
			if ref == "survey/read-specifics" && !cr.auth.Reader.CanReachProgram("summarize") {
				continue
			}
			out, err := gr.RenderWithReader(ctx, cr.auth.Reader, ref, nil)
			if err != nil {
				return err
			}
			sum.InjectAndGuidanceCount++
			viols := checkClaimsOnce(s, ref+" ("+cr.state.ID+")", out, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	if err := renderEditorActionPrompts(ctx, s, gr, wReps, sum); err != nil {
		return err
	}

	// Structural guidance fragments carry zero-tool authority.
	if err := renderStructuralGuidanceVariants(ctx, s, gr, zeroAuth, sum); err != nil {
		return err
	}

	// Zero-tool utility prompts must make zero callable tool claims.
	return renderZeroToolUtilityPrompts(ctx, s, ur, zeroAuth, sum)
}

type labeledAuth struct {
	label string
	auth  hoststate.TurnAuthority
}

func collectDistinctReachAuthorities(s *Stock, cReps []coordRep, wReps []workerRep) ([]labeledAuth, error) {
	var out []labeledAuth
	for _, i := range distinctReaderIndexes(len(cReps), func(i int) prompts.Reader { return cReps[i].auth.Reader }) {
		out = append(out, labeledAuth{label: cReps[i].state.ID, auth: cReps[i].auth})
	}
	for _, i := range distinctReaderIndexes(len(wReps), func(i int) prompts.Reader { return wReps[i].auth.Reader }) {
		out = append(out, labeledAuth{label: wReps[i].ws.ID, auth: wReps[i].auth})
	}
	// Narrowed recall and overlay projections no stock state produces, so the
	// recall and overlay guidance branches that read those facts render.
	if len(cReps) > 0 {
		baseAuth := cReps[0].auth
		for _, cr := range cReps {
			if cr.state.RootCount >= 1 && cr.state.SessionMode == api.SessionModeAgent && cr.state.Posture == api.SessionPostureBuild {
				baseAuth = cr.auth
				break
			}
		}
		var readerErr error
		mkSyntheticReachAuth := func(offered []string, metas []tools.ToolMeta, schemas map[string]map[string]any) hoststate.TurnAuthority {
			a := baseAuth
			a.Offered = offered
			a.Metas = metas
			a.Schemas = schemas
			a.PostLoadSchemas = nil
			a.Plan = toolsurface.Compile(offered, nil)
			a.Reach = toolreach.Offered(offered).WithSchemas(schemas)
			r, rErr := prompts.NewReader(prompts.HostFacts{
				Profiles:  s.Profiles,
				Schemas:   s.Schemas,
				RootCount: 1,
				HasGit:    true,
			}, prompts.TurnProjection{
				ProfileID:        prompts.CoordinatorProfileID,
				Plan:             a.Plan,
				Offered:          offered,
				ProjectedSchemas: schemas,
			})
			if rErr != nil {
				readerErr = rErr
			}
			a.Reader = r
			return a
		}
		partialRecallSchema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"history": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"after_ord":   map[string]any{"type": "integer"},
						"through_ord": map[string]any{"type": "integer"},
					},
				},
			},
		}
		narrowAuth := mkSyntheticReachAuth(
			[]string{"recall"},
			[]tools.ToolMeta{{Name: "recall", Description: "Recall session history.", ArgsSchema: partialRecallSchema}},
			map[string]map[string]any{"recall": partialRecallSchema},
		)
		out = append(out, labeledAuth{label: "recall-history-only-no-record-no-query", auth: narrowAuth})

		recordNoQuerySchema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"history": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"after_ord":   map[string]any{"type": "integer"},
						"through_ord": map[string]any{"type": "integer"},
						"search":      map[string]any{"type": "string"},
						"message_id":  map[string]any{"type": "string"},
						"offset":      map[string]any{"type": "integer"},
						"format":      map[string]any{"type": "string", "enum": []any{"prose", "record"}},
					},
				},
			},
		}
		recOnlyAuth := mkSyntheticReachAuth(
			[]string{"recall"},
			[]tools.ToolMeta{{Name: "recall", Description: "Recall history.", ArgsSchema: recordNoQuerySchema}},
			map[string]map[string]any{"recall": recordNoQuerySchema},
		)
		out = append(out, labeledAuth{label: "recall-record-without-query", auth: recOnlyAuth})

		previewMeta, _ := s.Schemas.ToolMeta("preview_overlay")
		instructMeta, _ := s.Schemas.ToolMeta("instruct_worker")
		previewNoPromote := mkSyntheticReachAuth(
			[]string{"preview_overlay", "instruct_worker"},
			[]tools.ToolMeta{
				{Name: "preview_overlay", Description: previewMeta.Description, ArgsSchema: previewMeta.ArgsSchema},
				{Name: "instruct_worker", Description: instructMeta.Description, ArgsSchema: instructMeta.ArgsSchema},
			},
			map[string]map[string]any{"preview_overlay": previewMeta.ArgsSchema, "instruct_worker": instructMeta.ArgsSchema},
		)
		out = append(out, labeledAuth{label: "preview-and-instruct-without-promote", auth: previewNoPromote})
		if readerErr != nil {
			return nil, readerErr
		}
	}
	return out, nil
}

func renderReachBoundGuidance(
	ctx context.Context,
	s *Stock,
	gr *prompts.GuidanceRenderer,
	stateLabel string,
	auth hoststate.TurnAuthority,
	sum *CartesianSummary,
) error {
	mints := auth.Reader.Mints()
	var evHandles, readHandles12, readHandles3 []string
	readHandle1 := ""
	if mints["read"] {
		readHandle1 = "read#1"
		readHandles12 = []string{"read#1", "read#2"}
		readHandles3 = []string{"read#3"}
		evHandles = append(evHandles, "read#1")
	}
	if mints["grep"] {
		evHandles = append(evHandles, "grep#2")
	}
	cases := []struct {
		ref  string
		data map[string]any
	}{
		{"compaction/command-exchange-reference", map[string]any{"command": "git status", "exit_code": 0, "after_ord": 1, "through_ord": 4}},
		{"compaction/tool-result-reference", map[string]any{"tool": "command", "evidence_handles": evHandles, "after_ord": 2, "through_ord": 5}},
		{"compaction/continuation-record", map[string]any{
			"level": "references", "omitted_from_ord": 1, "omitted_through_ord": 10, "through_ord": 12, "summary_through_ord": 8,
			"compacted_sources": []map[string]any{
				{"tool": "command", "handles": readHandles12, "message_id": "m-1"},
				{"tool": "command", "handles": nil, "message_id": "m-2"},
				{"tool": "", "handles": nil, "message_id": "m-3"},
				{"tool": "fetch_url", "handles": nil, "message_id": ""},
			},
			"reference_only": true,
			"recorded_details": []map[string]any{
				{"handle": readHandle1, "conclusion": "ok"},
				{"message_id": "m-2", "command": "cat -n a.go", "exit_code": 1, "verdict": "pass", "program_state": "exited", "stop_code": "NONE"},
				{"message_id": "m-3"},
			},
			"recorded_omitted": 2, "recorded_clipped": true,
		}},
		{"compaction/continuation-record", map[string]any{
			"level": "references", "omitted_through_ord": 0, "through_ord": 12, "recorded_omitted": 1, "recorded_clipped": false,
		}},
		{"compaction/continuation-record", map[string]any{
			"level": "references", "omitted_through_ord": 0, "through_ord": 0,
		}},
		{"compaction/continuation-record", map[string]any{
			"level": "summary", "through_ord": 12, "summary_through_ord": 8,
			"facts": []string{"Fact A"}, "completed": []string{"Step 1"}, "pending": []string{"Step 2"},
			"reacquire": []string{"Re-read a.go"}, "constraints": []string{"No external network"},
			"compacted_sources": []map[string]any{
				{"tool": "command", "handles": readHandles12, "message_id": "m-1"},
				{"tool": "", "handles": readHandles3, "message_id": "m-2"},
				{"tool": "command", "handles": nil, "message_id": "m-3"},
				{"tool": "", "handles": nil, "message_id": "m-4"},
			},
		}},
		{"compaction/continuation-record", map[string]any{
			"level": "summary", "through_ord": 10, "summary_through_ord": 10, "reference_only": false,
		}},
		{"compaction/continuation-record", map[string]any{
			"level": "summary", "through_ord": 0, "summary_through_ord": 0, "reference_only": false,
		}},
		{"enrich/overlay-rebase-conflict", map[string]any{"overlay_id": "ov-1", "worker": "impl-1", "conflict_paths": []string{"a.go", "b.go"}}},
		{"recall/body-missing", map[string]any{"handle": readHandle1, "message_id": "m-1", "after_ord": 1, "through_ord": 2}},
		{"recall/history-continuation", map[string]any{"after_ord": 5, "through_ord": 10}},
		{"recall/record-request", map[string]any{"message_id": "m-1", "after_ord": 1, "through_ord": 2}},
		{"utility/command-output-omitted", map[string]any{"complete": true, "total_lines": "400", "total_bytes": "16 KB", "saved_path": "@output/cmd.out", "quoted_saved_path": "@output/cmd.out", "next_start": 101, "next_end": 200}},
		{"utility/command-output-omitted", map[string]any{"complete": false, "total_lines": "400", "total_bytes": "16 KB", "saved_path": "@output/cmd.out", "quoted_saved_path": "@output/cmd.out", "next_start": 101, "next_end": 200}},
	}
	for i, tc := range cases {
		out, err := gr.RenderWithReach(ctx, auth.Reach, tc.ref, tc.data)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		if strings.TrimSpace(out) != "" {
			viols := checkClaimsOnce(s, fmt.Sprintf("guidance/%s[%d] (%s)", tc.ref, i, stateLabel), out, auth, "", "", toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}

func renderEditorActionPrompts(
	ctx context.Context,
	s *Stock,
	gr *prompts.GuidanceRenderer,
	wReps []workerRep,
	sum *CartesianSummary,
) error {
	var roRep, rwRep *workerRep
	for i := range wReps {
		if wReps[i].ws.ToolProfile == "explore_readonly" && wReps[i].ws.Posture == api.SessionPostureBuild && roRep == nil {
			roRep = &wReps[i]
		}
		if wReps[i].ws.ToolProfile == "editor_file" && wReps[i].ws.Posture == api.SessionPostureBuild && rwRep == nil {
			rwRep = &wReps[i]
		}
	}
	if rwRep == nil {
		for i := range wReps {
			if wReps[i].auth.Reader.Held()["command_mutation"] {
				rwRep = &wReps[i]
				break
			}
		}
	}
	occ := map[string]any{
		"path_display": "internal/foo/bar.go", "start_line": 10, "end_line": 25,
		"symbol": "LoadConfig", "new_name": "ReadConfig", "instruction": "ReadConfig",
		"rule_id": "G101", "finding_message": "hardcoded credential",
	}
	for _, ref := range []string{"editor/explain", "editor/summarize"} {
		if roRep == nil {
			break
		}
		out, err := gr.RenderWithReader(ctx, roRep.auth.Reader, ref, occ)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		viols := checkClaimsOnce(s, ref, out, roRep.auth, roRep.ws.ParentSessionMode, roRep.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
		sum.Violations = append(sum.Violations, viols...)
	}
	for _, ref := range []string{"editor/add-test", "editor/document", "editor/fix-finding", "editor/inline-edit", "editor/rename-symbol", "editor/rewrite-structurally"} {
		if rwRep == nil {
			break
		}
		out, err := gr.RenderWithReader(ctx, rwRep.auth.Reader, ref, occ)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		viols := checkClaimsOnce(s, ref, out, rwRep.auth, rwRep.ws.ParentSessionMode, rwRep.ws.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, true)
		sum.Violations = append(sum.Violations, viols...)
	}
	return nil
}

func renderStructuralGuidanceVariants(
	ctx context.Context,
	s *Stock,
	gr *prompts.GuidanceRenderer,
	zeroAuth hoststate.TurnAuthority,
	sum *CartesianSummary,
) error {
	cases := []struct {
		ref  string
		data map[string]any
	}{
		{"enrich/overlay-parent-rejected", map[string]any{"overlay_id": "ov-1", "parent_id": "ov-0"}},
		{"reject/_reject", map[string]any{"effect": "warn", "code": "W1", "what": "Warn", "cause": "Cause", "why": "Why", "fix": "Adjust.", "instead": "Stop.", "execution": map[string]any{"not_started": true}}},
		{"reject/_reject", map[string]any{"effect": "block", "code": "B1", "what": "Same", "cause": "Same", "why": "Same", "fix": "", "instead": "", "execution": map[string]any{"active": true}}},
		{"reject/_reject", map[string]any{"effect": "block", "code": "B2", "what": "What", "execution": map[string]any{"observed": true}}},
		{"reject/_reject", map[string]any{"effect": "block", "code": "B3", "what": "What"}},
		{"reject/spec-posture-block", map[string]any{"phase_required": true, "phase_name": "implement", "what": "W", "cause": "C", "why": "Y", "fix": "F", "instead": "I", "required": "R", "details": "D"}},
		{"reject/spec-posture-block", map[string]any{}},
		{"survey/grep-empty-structural", map[string]any{"files_parsed": 0}},
		{"survey/grep-empty-structural", map[string]any{"files_parsed": 5}},
		{"survey/grep-empty-text", map[string]any{"files_searched": 0}},
		{"survey/grep-empty-text", map[string]any{"files_searched": 8, "operators": "||"}},
		{"survey/grep-empty-text", map[string]any{"files_searched": 8}},
		{"survey/grep-scope-dense", map[string]any{"file_count": 42, "count": 100}},
		{"survey/grep-scope-dense", map[string]any{"file_count": 0, "count": 10}},
		{"survey/jq-zoom", map[string]any{"bytes": 8000}},
		{"utility/catalog-discovery", map[string]any{"total": 0, "has_more": false}},
		{"utility/catalog-discovery", map[string]any{"total": 5, "has_more": true}},
		{"utility/catalog-discovery", map[string]any{"total": 3, "has_more": false}},
		{"utility/source-remove-permanent", map[string]any{"files": 1, "recovery_known": false, "recovery_bytes": 0, "content_bytes": 100}},
		{"utility/source-remove-permanent", map[string]any{"files": 2, "recovery_known": true, "recovery_bytes": 50, "content_bytes": 100}},
		{"utility/source-remove-permanent", map[string]any{"files": 2, "recovery_known": true, "recovery_bytes": 100, "content_bytes": 100}},
	}
	for i, tc := range cases {
		out, err := gr.Render(ctx, tc.ref, tc.data)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		if strings.TrimSpace(out) != "" {
			viols := checkClaimsOnce(s, fmt.Sprintf("structural-guidance/%s[%d]", tc.ref, i), out, zeroAuth, "", "", toolvocabtest.SubjectSelfNow(), nil, nil, nil, false)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}

func renderZeroToolUtilityPrompts(
	ctx context.Context,
	s *Stock,
	ur *prompts.UtilityPromptRenderer,
	zeroAuth hoststate.TurnAuthority,
	sum *CartesianSummary,
) error {
	dataA := map[string]any{
		"query": "auth", "task_hint": "login flow", "max_seeds": 3, "max_hosts": 4,
		"min_leads": 2, "max_leads": 5, "expand_min": 2, "expand_max": 4,
		"hit_target": 5, "period_current": true, "period": "current",
		"weekday": "Saturday", "date": "2026-08-15", "cutoff": "2025-01",
		"goal": "ship", "tool": "command",
		"steps": "1. ls", "receipts": "1 ok", "output": "", "call_result": "",
		"command": "ls", "movement": "checkout feat", "diffs": "",
		"user_intent": "ship", "worker_brief": "fix", "tracked_plan": "- [x] 1",
		"recent_results": "ok", "action": "verify",
		"phase_scope": "", "host_facts": "", "assistant_reasoning": "", "assistant_note": "",
		"result_handle": "res-1", "worker": "impl-1", "round": 1, "assignment": "fix",
		"outcome": "complete", "report": "done", "reasoning": "verified",
		"current_request": "continue", "prior_continuation": "{}",
		"recent_messages": []map[string]any{
			{"role": "user", "content": "hi", "compaction_strategy": "summary", "evidence_handles": []string{"read#1", "read#2"}, "host_secret_redaction": true, "host_secret_references": []string{"ref-1"}},
			{"role": "assistant", "content": "ok"},
		},
		"focus": "entry", "budget": 3,
		"candidates": []map[string]any{
			{"handle": "read#1", "path": "a.go", "lines": "1-10", "preview": "package a"},
			{"handle": "read#2", "path": "b.go", "lines": "", "preview": "package b"},
		},
	}
	dataB := map[string]any{
		"query": "q", "task_hint": "", "max_seeds": 2, "max_hosts": 2,
		"min_leads": 1, "max_leads": 2, "expand_min": 1, "expand_max": 2,
		"hit_target": 2, "period_current": false, "period": "2024",
		"weekday": "Sunday", "date": "2026-08-16", "cutoff": "",
		"goal": "ship", "tool": "command",
		"steps": "", "receipts": "", "output": "main.go", "call_result": "done",
		"command": "", "movement": "", "diffs": "+1 -0",
		"user_intent": "", "worker_brief": "", "tracked_plan": "", "recent_results": "", "action": "",
		"phase_scope": "execute", "host_facts": "clean", "assistant_reasoning": "sound", "assistant_note": "ready",
		"result_handle": "res-2", "worker": "", "round": 0, "assignment": "",
		"outcome": "", "report": "", "reasoning": "stopped early",
		"current_request": "", "prior_continuation": "{}",
		"focus": "none", "budget": 1,
	}
	for _, ref := range prompts.ZeroToolUtilityTemplates() {
		variants := []map[string]any{dataA, dataB}
		if ref == "utility/worker-result-user" {
			variants = append(variants, map[string]any{"result_handle": "res-3", "reasoning": ""})
		}
		for dIdx, d := range variants {
			out, err := ur.Render(ctx, ref, d)
			if err != nil {
				return err
			}
			sum.InjectAndGuidanceCount++
			viols := checkClaimsOnce(s, fmt.Sprintf("zero-tool-utility/%s[%d]", ref, dIdx), out, zeroAuth, "", "", toolvocabtest.SubjectSelfNow(), nil, nil, nil, false)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}

func runGateFeedbackRenders(
	ctx context.Context,
	s *Stock,
	sum *CartesianSummary,
) error {
	gateCat, err := feedback.LoadGateFeedbackCatalogWithCatalog(s.Catalog)
	if err != nil {
		return err
	}
	// Render with matched signals present and absent so both gate branches run.
	leaves := []string{
		"plan_stub_valid", "hitl_consulted:intake", "evidence_passed:plan_review",
		"evidence_passed:survey_claims", "evidence_passed:survey_challenged",
		"obligation_settled:scan", "options_selection_valid", "fanout_planned",
		"topology_stage_complete", "topology_report_delivered", "human_approval",
		"child_run_complete", "choice_transition_required", "closeout_gates_passed",
		"delivery_gates_passed", "orchestration_complete", "recon_or_board_ready",
		"research_satisfied", "worker_cycle_ready",
	}
	for _, leaf := range leaves {
		for _, withSignals := range []bool{true, false} {
			fCtx := map[string]any{}
			if withSignals {
				fCtx = feedback.PlanStubGateExtras("")
				fCtx["advance_when_gate_met"] = "coordinator"
				fCtx["review_agents"] = []string{"skeptic"}
			}
			out, rErr := gateCat.RenderGateFeedback(ctx, leaf, fCtx)
			if rErr != nil {
				return rErr
			}
			if strings.TrimSpace(out) != "" {
				sum.InjectAndGuidanceCount++
			}
		}
	}
	return nil
}

func zeroToolAuthority(base hoststate.TurnAuthority) hoststate.TurnAuthority {
	out := base
	out.Metas = nil
	out.Offered = nil
	out.Schemas = map[string]map[string]any{}
	out.PostLoadSchemas = nil
	out.ProgramReach = nil
	out.ProgramTable = nil
	out.Reach = toolreach.Offered(nil)
	return out
}
