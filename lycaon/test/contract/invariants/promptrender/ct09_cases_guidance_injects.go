package promptrender

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// viewContextCases renders inject.RenderViewContextBlock over each view part
// present or absent.
func viewContextCases(_ context.Context, _ *guidanceCaseEnv) ([]guidanceCase, error) {
	knobs := []casegen.Knob{
		indexKnob("editors", []string{"editors"}, []string{"", "one"}),
		indexKnob("diffs", []string{"diffs"}, []string{"", "one"}),
		indexKnob("folder", []string{"folder"}, []string{"", "set"}),
		indexKnob("search", []string{"search_query", "search_hit"}, []string{"", "query", "hit"}),
		indexKnob("findings", []string{"findings"}, []string{"", "one"}),
	}
	return []guidanceCase{{
		unit: "guidance/view-context", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			var view api.ViewContext
			if knobValue[int](knobs, set, "editors") == 1 {
				view.Editors = []api.ViewEditor{{DisplayPath: "a.go", Selection: &api.ViewSelection{StartLine: 2, EndLine: 4}}}
			}
			if knobValue[int](knobs, set, "diffs") == 1 {
				view.Diffs = []api.ViewPath{{DisplayPath: "a.go"}}
			}
			if knobValue[int](knobs, set, "folder") == 1 {
				view.Folder = &api.ViewPath{DisplayPath: "src/"}
			}
			switch knobValue[int](knobs, set, "search") {
			case 1:
				view.Search = &api.ViewSearch{Query: "main"}
			case 2:
				view.Search = &api.ViewSearch{Query: "main", Hit: &api.ViewLocation{Path: "src/main.go", DisplayPath: "src/main.go", Line: 3}}
			}
			if knobValue[int](knobs, set, "findings") == 1 {
				view.Findings = []api.ViewFinding{{RuleID: "R1", Level: api.FindingLevel("high")}}
			}
			return e.unscopedRender(inject.RenderViewContextBlock(ctx, e.inj, "sess-view", view)), nil
		},
	}}, nil
}

// workerRoundCases renders inject.RenderWorkerRoundBlock over the round
// frames workerrecords produces: the first round, and later rounds that
// retained, refreshed or reset the workspace after the prior change settled.
func workerRoundCases(_ context.Context, _ *guidanceCaseEnv) ([]guidanceCase, error) {
	rounds := []struct {
		label, workspace, prior string
	}{
		{"first", "", ""},
		{"retained", "retained", ""},
		{"retained-open", "retained", "open"},
		{"retained-shelved", "retained", "shelved"},
		{"retained-landed", "retained", "landed"},
		{"refreshed", "refreshed", "landed"},
		{"reset", "reset", "rejected"},
	}
	labels := make([]string, len(rounds))
	for i, r := range rounds {
		labels[i] = r.label
	}
	knobs := []casegen.Knob{
		indexKnob("brief", []string{"has_brief"}, []string{"brief", "none"}),
		indexKnob("instructions", []string{"instruction_count"}, []string{"two", "none"}),
		indexKnob("round", []string{"workspace", "prior_change", "earlier_results"}, labels),
	}
	return []guidanceCase{{
		unit: "guidance/worker-round", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			r := rounds[knobValue[int](knobs, set, "round")]
			frame := inject.WorkerRoundFrame{Ordinal: 1, Workspace: r.workspace, PriorChange: r.prior}
			if r.workspace != "" {
				frame.Ordinal, frame.EarlierResults = 2, []string{"res-1"}
			}
			if knobValue[int](knobs, set, "brief") == 0 {
				frame.Brief = "Fix the parser."
			}
			if knobValue[int](knobs, set, "instructions") == 0 {
				frame.Instructions = []inject.WorkerMessageLine{{Worker: "coordinator", Seq: 1, Body: "Start with tests."}, {Worker: "coordinator", Seq: 2, Body: "Keep the API."}}
			}
			block := inject.RenderWorkerRoundBlock(ctx, e.inj, "sess-round", frame)
			return e.unscopedRender(block.Header), nil
		},
	}}, nil
}

// scanListStub serves the session's scans to the guidance provider.
type scanListStub struct {
	scan.ScanCoordinator
	scans []api.CodeScan
}

func (s scanListStub) ListBySessionID(context.Context, string) ([]api.CodeScan, error) {
	return s.scans, nil
}

// scanGuidanceCases renders scan guidance through scan.GuidanceProvider from
// a completed security scan's guidance rows.
func scanGuidanceCases(_ context.Context, _ *guidanceCaseEnv) ([]guidanceCase, error) {
	knobs := []casegen.Knob{
		{Name: "count", Facts: []string{"item.count"}, Values: []any{3, 1}},
		indexKnob("fix", []string{"item.fix"}, []string{"fix", "none"}),
	}
	return []guidanceCase{{
		unit: "guidance/scan-guidance-ephemeral", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			row := api.ScanGuidanceSummary{Code: "R1", Message: "Untrusted input reaches a query.", Count: knobValue[int](knobs, set, "count")}
			if knobValue[int](knobs, set, "fix") == 0 {
				row.Fix = "Validate input."
			}
			p := scan.NewGuidanceProvider(scanListStub{scans: []api.CodeScan{{
				Status: api.CodeScanStatusComplete, Categories: []api.ScanCategory{api.ScanCategorySecurity},
				Guidance: []api.ScanGuidanceSummary{row}, CreatedAt: e.now,
			}}}, scancfg.DefaultAgentBudget())
			p.SetInjectRenderer(e.inj)
			msgs := p.Prepend(ctx, "sess-scan", nil)
			if len(msgs) == 0 {
				return guidanceRender{}, nil
			}
			return e.unscopedRender(msgs[0].Content), nil
		},
	}}, nil
}

// rejectCases renders the reject template through its two producers: the
// stock hint catalog through guidance.StaticRejectFormatter, one code per
// distinct effect, informational category and banner emit, and frozen OAR
// copy through guidance.RenderPolicyCopy for each effect that renders copy.
// Neither producer has a recipient, so the renders carry no authority to check.
func rejectCases(_ context.Context, _ *guidanceCaseEnv) ([]guidanceCase, error) {
	hints, err := guidance.LoadHintConfigStock()
	if err != nil {
		return nil, err
	}
	codes := make([]string, 0, len(hints.HintCodes))
	for code := range hints.HintCodes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	var labels []string
	seen := map[string]bool{}
	for _, code := range codes {
		h := hints.HintCodes[code]
		key := fmt.Sprintf("%s|%t|%t", strings.TrimSpace(h.Effect), strings.TrimSpace(h.Category) == "informational", strings.TrimSpace(h.Emit) == guidance.EmitBanner)
		if !seen[key] {
			seen[key] = true
			labels = append(labels, "hint:"+code)
		}
	}
	// Copy-rendering OAR effects with a shipped rule of each.
	for _, oar := range []string{"block:APPROVAL_RULE_DENIED", "warn:SANDBOX_TRY_READ_PATH", "nudge:AMBIENT_GROUNDING_ESCALATED"} {
		labels = append(labels, "oar:"+oar)
	}
	knobs := []casegen.Knob{indexKnob("source", []string{casegen.AnyFact}, labels)}
	formatter := guidance.NewStaticRejectFormatter(hints)
	return []guidanceCase{{
		unit: "guidance/reject/_reject.md", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			label := labels[knobValue[int](knobs, set, "source")]
			if code, ok := strings.CutPrefix(label, "hint:"); ok {
				text, err := formatter.Format(code, map[string]any{})
				if err != nil {
					return guidanceRender{}, err
				}
				return guidanceRender{text: text, recipientless: true}, nil
			}
			effect, code, _ := strings.Cut(strings.TrimPrefix(label, "oar:"), ":")
			text, err := guidance.RenderPolicyCopy(ctx, code, effect, map[string]string{"what": "The call was refused.", "cause": "A rule matched.", "fix": "Adjust the call."}, nil)
			if err != nil {
				return guidanceRender{}, err
			}
			return guidanceRender{text: text, recipientless: true}, nil
		},
	}}, nil
}

// continuationRecordCases renders the continuation record as compaction
// renders it: the delivered references and summary levels, whose
// through_ord covers every compacted source, and the session revision digest,
// which renders the summary level with no ordinal. A record-capable recall
// reach comes from a stock recipient offering recall and minting read handles.
func continuationRecordCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	var recall *coordRep
	for i, cr := range e.cReps {
		if cr.auth.Reach.Status("recall") == toolreach.Callable && cr.auth.Reach.AcceptsField("recall", "history.format") && cr.auth.Reader.Mints()["read"] {
			recall = &e.cReps[i]
			break
		}
	}
	if recall == nil {
		return nil, fmt.Errorf("no stock coordinator offers recall with history records")
	}
	knobs := []casegen.Knob{
		repKnob([]string{"can_recall_history", "can_recall_history_record", "can_recall_query"}, 2, func(i int) string { return []string{"recall", "none"}[i] }),
		indexKnob("record", []string{"through_ord"}, []string{"summary", "references", "revision"}),
		indexKnob("source", []string{"s.handles", "s.tool"}, []string{"message", "handles", "message-without-tool"}),
	}
	return []guidanceCase{{
		unit: "guidance/compaction/continuation-record", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			reach, auth := recall.auth.Reach, recall.auth
			if knobValue[int](knobs, set, "rep") == 1 {
				reach, auth = toolreach.Offered(nil), e.zeroAuth()
			}
			source := map[string]any{"tool": "command", "handles": []string(nil), "message_id": "m-3"}
			switch knobValue[int](knobs, set, "source") {
			case 1:
				source = map[string]any{"tool": "command", "handles": []string{"read#1"}, "message_id": "m-1"}
			case 2:
				source = map[string]any{"tool": "", "handles": []string(nil), "message_id": "m-4"}
			}
			data := map[string]any{"level": "summary", "through_ord": 12, "summary_through_ord": 8, "facts": []string{"Fact A"}, "compacted_sources": []map[string]any{source}}
			switch knobValue[int](knobs, set, "record") {
			case 1:
				data = map[string]any{"level": "references", "through_ord": 12, "compacted_sources": []map[string]any{source}}
			case 2:
				data = map[string]any{"level": "", "facts": []string{"revision"}, "compacted_sources": []map[string]any{source}}
			}
			text, err := e.gr.RenderWithReach(ctx, reach, "compaction/continuation-record", data)
			if err != nil {
				return guidanceRender{}, err
			}
			return guidanceRender{text: text, auth: auth, backticks: true}, nil
		},
	}}, nil
}

// sourceChangesCases renders inject.RenderSourceChangesBlock on stock
// coordinators with folders, mutation-capable or not, over briefs
// sourcebrief builds: a git move or a checkout from a detached head, with
// or without itemized files, and either overflow class.
func sourceChangesCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := coordinatorRepsBy(e.cReps,
		func(cr coordRep) bool { return cr.state.RootCount > 0 },
		func(cr coordRep) string {
			return fmt.Sprintf("%t|%s", readerFact(cr.auth.Reader, "profile_mutation_capable"), offers(cr.auth, "source_history"))
		})
	slices.SortStableFunc(reps, func(a, b coordRep) int {
		return boolOrder(readerFact(b.auth.Reader, "profile_mutation_capable")) - boolOrder(readerFact(a.auth.Reader, "profile_mutation_capable"))
	})
	if len(reps) == 0 {
		return nil, fmt.Errorf("no stock coordinator with folders")
	}
	knobs := []casegen.Knob{
		repKnob([]string{"profile_mutation_capable", "profile_has_source_history"}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("git", []string{"g.from_ref", "g.from_ref != g.to_ref"}, []string{"move", "detached", "none"}),
		indexKnob("files", []string{"files"}, []string{"one", "none"}),
		indexKnob("overflow", []string{"truncated", "own_truncated"}, []string{"none", "foreign", "own", "both"}),
	}
	return []guidanceCase{{
		unit: "guidance/source-changes", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			var brief inject.SourceChangeBrief
			switch knobValue[int](knobs, set, "git") {
			case 0:
				brief.Git = []inject.SourceGitLine{{Kind: "checkout", FromRef: "main", ToRef: "feat", FromCommit: "111", ToCommit: "222", At: "14:00 UTC"}}
			case 1:
				brief.Git = []inject.SourceGitLine{{Kind: "checkout", ToRef: "feat", ToCommit: "222", At: "14:00 UTC"}}
			}
			if knobValue[int](knobs, set, "files") == 0 {
				brief.Files = []inject.SourceChangeFile{{Path: "a.go", Actor: "user", Op: "modify", At: "14:01 UTC", Effects: 1}}
			}
			ov := knobValue[int](knobs, set, "overflow")
			brief.Truncated, brief.OwnTruncated = ov == 1 || ov == 3, ov == 2 || ov == 3
			return coordinatorRender(inject.RenderSourceChangesBlock(ctx, e.inj, cr.auth.Reader, "sess-sc", brief), cr), nil
		},
	}}, nil
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

// commandJobsCases renders inject.RenderCommandJobsBlock for each ledger
// shape: running, exited and held rows alone, and running with held. A
// coordinator's authority is projected with the live resources, which
// promote the resources' control tools.
func commandJobsCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	shapes := []string{"running", "exited", "held", "running-held"}
	jobsFor := func(shape string) ([]bgprocess.JobSnapshot, []heldcall.Running) {
		running := bgprocess.JobSnapshot{Handle: "cmd-run", Mode: bgprocess.JobModeAwaited, OriginTool: "command", StartedAt: e.now.Add(-30 * time.Second), Timeout: 2 * time.Minute, Stages: []hostcmd.StageResult{{Command: "go test ./..."}}}
		exited := bgprocess.JobSnapshot{Handle: "cmd-exit", Mode: bgprocess.JobModeBackground, OriginTool: "command", StartedAt: e.now.Add(-2 * time.Minute), Exited: true, ExitCode: 1}
		held := []heldcall.Running{{Handle: "held-1", Tool: "command", Elapsed: 15 * time.Second}}
		switch shape {
		case "running":
			return []bgprocess.JobSnapshot{running}, nil
		case "exited":
			return []bgprocess.JobSnapshot{exited}, nil
		case "held":
			return nil, held
		}
		return []bgprocess.JobSnapshot{running}, held
	}
	jobFacts := []string{"has_running", "has_exited", "has_held"}
	repFacts := []string{"wait_process_done_allowed", casegen.AnyFact}

	coords := coordinatorRepsBy(e.cReps,
		func(cr coordRep) bool { return slices.Contains(cr.auth.Offered, "command") },
		func(cr coordRep) string {
			return offers(cr.auth, "wait", "command_output", "command_stop", "held_result", "held_stop")
		})
	type authKey struct {
		rep        int
		jobs, held bool
	}
	auths := map[authKey]hoststate.TurnAuthority{}
	coordKnobs := []casegen.Knob{
		repKnob(repFacts, len(coords), func(i int) string { return coords[i].state.ID }),
		indexKnob("jobs", jobFacts, shapes),
	}
	var workers []workerRep
	seen := map[string]bool{}
	for _, wr := range e.wReps {
		key := offers(wr.auth, "wait", "command_output", "command_stop", "held_result", "held_stop")
		if wr.ws.Overlay.ID == "stock" && slices.Contains(wr.auth.Offered, "command") && !seen[key] {
			seen[key] = true
			workers = append(workers, wr)
		}
	}
	workerKnobs := []casegen.Knob{
		repKnob(repFacts, len(workers), func(i int) string { return workers[i].ws.ID }),
		indexKnob("jobs", jobFacts, shapes),
	}
	return []guidanceCase{
		{
			unit: "guidance/command-jobs", recipient: interactions.RecipientCoordinator, knobs: coordKnobs,
			render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
				idx := knobValue[int](coordKnobs, set, "rep")
				jobs, held := jobsFor(shapes[knobValue[int](coordKnobs, set, "jobs")])
				key := authKey{idx, len(jobs) > 0, len(held) > 0}
				auth, ok := auths[key]
				if !ok {
					a, err := hoststate.FreezeTurnAuthorityWithResources(ctx, coords[idx].state, toolcontract.ResourcePresence{CommandJobs: key.jobs, HeldCalls: key.held})
					if err != nil {
						return guidanceRender{}, err
					}
					auth, auths[key] = a, a
				}
				cr := coordRep{state: coords[idx].state, auth: auth}
				return coordinatorRender(inject.RenderCommandJobsBlock(ctx, e.inj, auth.Reader, auth.Schemas["wait"], "sess-jobs", jobs, held, e.now, "coordinator"), cr), nil
			},
		},
		{
			unit: "guidance/command-jobs", recipient: interactions.RecipientWorker, knobs: workerKnobs,
			render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
				wr := workers[knobValue[int](workerKnobs, set, "rep")]
				jobs, held := jobsFor(shapes[knobValue[int](workerKnobs, set, "jobs")])
				return workerRender(inject.RenderCommandJobsBlock(ctx, e.inj, wr.auth.Reader, wr.auth.Schemas["wait"], "wsess-jobs", jobs, held, e.now, "worker"), wr), nil
			},
		},
	}, nil
}

// executionStateCases render inject.RenderExecutionStateMessage over
// transcript windows after one user intent, in one tool batch: rejected
// writes to distinct paths and failed commands, each none, one, or more than
// the row cap, with and without a declared project check.
func executionStateCases(_ context.Context, _ *guidanceCaseEnv) ([]guidanceCase, error) {
	knobs := []casegen.Knob{
		{Name: "operations", Facts: []string{"operations", "operations_omitted"}, Values: []any{1, 0, 4}, Labels: []string{"one", "none", "over-cap"}},
		{Name: "executions", Facts: []string{"executions", "executions_omitted"}, Values: []any{0, 1, 4}, Labels: []string{"none", "one", "over-cap"}},
		indexKnob("declared", []string{"declared_command"}, []string{"none", "declared"}),
	}
	return []guidanceCase{{
		unit: "guidance/execution-state", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			window := []api.Message{{ID: "u-1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Fix the build."}}
			result := func(tool string, i int, args map[string]any, receipt api.InvocationReceipt) api.Message {
				call := fmt.Sprintf("%s-%d", tool, i)
				receipt.ID, receipt.Tool, receipt.ToolCallID, receipt.ArgsDigest = "inv-"+call, tool, call, "digest-"+call
				return api.Message{ID: "m-" + call, Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
					Tool: tool, ToolCallID: call, AssistantMessageID: "a-1", ToolArgs: args, Invocation: &receipt,
				}}
			}
			for i := range knobValue[int](knobs, set, "operations") {
				window = append(window, result("write", i, map[string]any{"path": fmt.Sprintf("src/f%d.go", i)},
					api.InvocationReceipt{Status: api.InvocationStatusRejected, Failure: &api.InvocationFailure{Code: "PATH_DENIED"}}))
			}
			for i := range knobValue[int](knobs, set, "executions") {
				window = append(window, result("command", i, map[string]any{"command": fmt.Sprintf("go test ./pkg%d", i), "cwd": "@primary"},
					api.InvocationReceipt{Status: api.InvocationStatusCompleted, Invoked: true, SourceVerdict: api.SourceVerdictFailed}))
			}
			declared := ""
			if knobValue[int](knobs, set, "declared") == 1 {
				declared = "./task check"
			}
			msg := inject.RenderExecutionStateMessage(ctx, e.inj, "sess-exec", window, declared)
			return e.unscopedRender(msg.Content), nil
		},
	}}, nil
}

// specPostureOutcome is a posture-rule deny as rules.outcomeFromThen reads
// it from the bundled host/posture-rules.
type specPostureOutcome struct{ code, phase, name string }

func (o specPostureOutcome) GetRejectCode() string        { return o.code }
func (o specPostureOutcome) GetPhaseRequired() string     { return o.phase }
func (o specPostureOutcome) GetPhaseRequiredName() string { return o.name }
func (o specPostureOutcome) GetMinRequired() string       { return "" }
func (o specPostureOutcome) GetMaxPlaybook() string       { return "" }

// specPostureCases render guidance.RenderSpecPostureRejectBlock for denies
// that name their required phase, as most spec rules do, and one that gives
// only its number (host/posture-rules/spec.yaml declares both).
func specPostureCases(_ context.Context, _ *guidanceCaseEnv) ([]guidanceCase, error) {
	knobs := []casegen.Knob{indexKnob("phase", []string{"phase_required", "phase_name"}, []string{"named", "numbered"})}
	return []guidanceCase{{
		unit: "guidance/reject/spec-posture-block", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			out, tool := specPostureOutcome{code: "SPEC_POSTURE_UNRESOLVED", phase: "1", name: "Research depth (stub blueprint)"}, "write"
			if knobValue[int](knobs, set, "phase") == 1 {
				out, tool = specPostureOutcome{code: "SPEC_POSTURE_DELEGATION_FORBIDDEN", phase: "1"}, "instruct_worker"
			}
			text, err := guidance.RenderSpecPostureRejectBlock(ctx, "sess-spec", tool, out, guidance.PlanProgress{},
				map[string]string{"what": "Writes wait for the research phase.", "fix": "Finish the research phase first."}, nil)
			if err != nil {
				return guidanceRender{}, err
			}
			return guidanceRender{text: text, recipientless: true}, nil
		},
	}}, nil
}

// boardOrientationCases render inject.RenderBoardOrientationInject for a
// two-folder project whose second folder's brief fits the budget, beside one
// the board marked truncated.
func boardOrientationCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := coordinatorRepsBy(e.cReps, func(cr coordRep) bool { return cr.state.RootCount > 1 },
		func(cr coordRep) string { return offers(cr.auth, "pack_board") })
	if len(reps) == 0 {
		return nil, fmt.Errorf("no stock coordinator with two folders")
	}
	knobs := []casegen.Knob{
		repKnob([]string{casegen.AnyFact}, len(reps), func(i int) string { return reps[i].state.ID }),
		indexKnob("secondary", []string{"root.truncated"}, []string{"fits", "truncated"}),
	}
	return []guidanceCase{{
		unit: "guidance/board-orientation", recipient: interactions.RecipientCoordinator, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			cr := reps[knobValue[int](knobs, set, "rep")]
			snap := api.BoardSnapshot{
				Repo: api.RepoBrief{Languages: []string{"Go"}, FileCount: 12},
				OrientationRoots: []api.BoardOrientationRoot{
					{Label: "primary", IsPrimary: true, Brief: api.RepoBrief{Languages: []string{"Go"}, FileCount: 12}},
					{Label: "docs", Brief: api.RepoBrief{Languages: []string{"Markdown"}, FileCount: 3}, Truncated: knobValue[int](knobs, set, "secondary") == 1},
				},
			}
			text, err := inject.RenderBoardOrientationInject(ctx, e.inj, cr.auth.Reader, "sess-board-ct09", snap, packboard.InjectScopeFull, false, false, e.now)
			if err != nil {
				return guidanceRender{}, err
			}
			return coordinatorRender(text, cr), nil
		},
	}}, nil
}

// workerRepsBy keeps the first stock worker recipient of each distinct key.
func workerRepsBy(reps []workerRep, keep func(workerRep) bool, key func(workerRep) string) []workerRep {
	var out []workerRep
	seen := map[string]bool{}
	for _, wr := range reps {
		if wr.ws.Overlay.ID != "stock" || !keep(wr) {
			continue
		}
		if k := key(wr); !seen[k] {
			seen[k] = true
			out = append(out, wr)
		}
	}
	return out
}

// workerTaskAssignmentCases render inject.RenderWorkerTaskAssignment for read
// and write scopes (a write leg goes to a mutation-capable worker), each
// overlay view kind, a review-coverage binding that does or does not require
// coverage, and a scope path the outline reads from this package's sources.
func workerTaskAssignmentCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := workerRepsBy(e.wReps, func(workerRep) bool { return true },
		func(wr workerRep) string {
			return fmt.Sprintf("%t|%s", wr.auth.Reader.Held()["command_mutation"], offers(wr.auth, "read_review", "scan_query", "record_finding", "read_findings", "request_decision"))
		})
	knobs := []casegen.Knob{
		repKnob([]string{casegen.AnyFact}, len(reps), func(i int) string { return reps[i].ws.ID }),
		indexKnob("mode", []string{"scope_mode", "scope_has_git", "linked_checkout"}, []string{"read", "write"}),
		{Name: "view", Facts: []string{"view_kind"}, Values: []any{"", "mounted"}, Labels: []string{"default", "mounted"}},
		indexKnob("coverage", []string{"coverage_assignment", "coverage_required"}, []string{"none", "required", "optional"}),
		indexKnob("paths", []string{"file_orientations", "file.symbols_elided"}, []string{"none", "file"}),
	}
	return []guidanceCase{{
		unit: "guidance/worker-task-assignment", recipient: interactions.RecipientWorker, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			wr := reps[knobValue[int](knobs, set, "rep")]
			in := inject.WorkerTaskAssignmentInput{
				Reader: wr.auth.Reader, SessionID: "wsess-assign", ProjectDir: ".", AgentType: wr.ws.AgentID,
				WorkerName: "w-alpha", WorkerJobID: "job-1", ViewKind: knobValue[string](knobs, set, "view"),
				Charter: api.WorkerTaskCharter{Goal: "Complete the assigned slice.", DoneWhen: []string{"Verified"}},
				Scope:   api.TaskScope{Mode: api.TaskScopeModeRead},
			}
			if knobValue[int](knobs, set, "mode") == 1 {
				if !wr.auth.Reader.Held()["command_mutation"] {
					return guidanceRender{}, nil
				}
				in.Scope.Mode = api.TaskScopeModeWrite
			}
			if knobValue[int](knobs, set, "paths") == 1 {
				in.Scope.Paths = []string{"overlay_skills.go"}
			}
			switch knobValue[int](knobs, set, "coverage") {
			case 1:
				in.CoverageAssignment = &reviewcoverage.Binding{ID: "cov-1", RunID: "run-1", Phase: "challenge", WorkID: "work-1", Agent: wr.ws.AgentID, CoverageRequired: true}
			case 2:
				in.CoverageAssignment = &reviewcoverage.Binding{ID: "cov-2", RunID: "run-1", Phase: "challenge", WorkID: "work-2", Agent: wr.ws.AgentID}
			}
			text, err := inject.RenderWorkerTaskAssignment(ctx, e.inj, in)
			if err != nil {
				return guidanceRender{}, err
			}
			return workerRender(text, wr), nil
		},
	}}, nil
}

// workerLegCases render inject.RenderWorkerLegInject with and without a
// result under review, isolated or not, over each overlay view kind.
func workerLegCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	reps := workerRepsBy(e.wReps, func(workerRep) bool { return true },
		func(wr workerRep) string { return offers(wr.auth, "record_finding", "read_findings", "recall") })
	knobs := []casegen.Knob{
		repKnob([]string{casegen.AnyFact}, len(reps), func(i int) string { return reps[i].ws.ID }),
		indexKnob("review", []string{"review_handle"}, []string{"none", "result"}),
		indexKnob("isolation", []string{"requires_isolation"}, []string{"isolated", "shared"}),
		{Name: "view", Facts: []string{"view_kind"}, Values: []any{"materialized", "mounted"}},
	}
	return []guidanceCase{{
		unit: "guidance/worker-leg", recipient: interactions.RecipientWorker, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			wr := reps[knobValue[int](knobs, set, "rep")]
			lc := inject.WorkerLegContext{
				LegID: "leg-1", AgentType: wr.ws.AgentID, WorkflowID: "implement", PhaseID: "execute",
				RequiresIsolation: knobValue[int](knobs, set, "isolation") == 0,
				LegTools:          wr.auth.Offered, ViewKind: knobValue[string](knobs, set, "view"),
			}
			if knobValue[int](knobs, set, "review") == 1 {
				lc.ReviewHandle = "rev-1"
			}
			text, err := inject.RenderWorkerLegInject(ctx, e.inj, wr.auth.Reader, "wsess-leg", lc)
			if err != nil {
				return guidanceRender{}, err
			}
			return workerRender(text, wr), nil
		},
	}}, nil
}
