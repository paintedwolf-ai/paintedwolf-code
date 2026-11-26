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
				return guidanceRender{text: text, auth: e.zeroAuth()}, nil
			}
			effect, code, _ := strings.Cut(strings.TrimPrefix(label, "oar:"), ":")
			text, err := guidance.RenderPolicyCopy(ctx, code, effect, map[string]string{"what": "The call was refused.", "cause": "A rule matched.", "fix": "Adjust the call."}, nil)
			if err != nil {
				return guidanceRender{}, err
			}
			return guidanceRender{text: text, auth: e.zeroAuth()}, nil
		},
	}}, nil
}

// continuationRecordCases renders the continuation record as compaction
// renders it: the delivered references and summary levels, whose
// through_ord covers every compacted source, and the session revision digest,
// which renders the summary level with no ordinal. A record-capable recall
// reach comes from a stock recipient offering recall.
func continuationRecordCases(_ context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
	var recall *toolreach.Request
	for _, cr := range e.cReps {
		if cr.auth.Reach.Status("recall") == toolreach.Callable && cr.auth.Reach.AcceptsField("recall", "history.format") {
			r := cr.auth.Reach
			recall = &r
			break
		}
	}
	if recall == nil {
		return nil, fmt.Errorf("no stock coordinator offers recall with history records")
	}
	knobs := []casegen.Knob{
		indexKnob("reach", []string{"can_recall_history", "can_recall_history_record", "can_recall_query"}, []string{"recall", "none"}),
		indexKnob("record", nil, []string{"summary", "references", "revision"}),
		indexKnob("source", []string{"s.handles", "s.tool"}, []string{"message", "handles", "message-without-tool"}),
	}
	return []guidanceCase{{
		unit: "guidance/compaction/continuation-record", recipient: unscopedRecipient, knobs: knobs,
		render: func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error) {
			reach := *recall
			if knobValue[int](knobs, set, "reach") == 1 {
				reach = toolreach.Offered(nil)
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
			return e.unscopedRender(text), nil
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
		repKnob("rep", []string{"profile_mutation_capable", "profile_has_source_history"}, len(reps), func(i int) string { return reps[i].state.ID }),
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
func commandJobsCases(ctx context.Context, e *guidanceCaseEnv) ([]guidanceCase, error) {
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
		repKnob("rep", repFacts, len(coords), func(i int) string { return coords[i].state.ID }),
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
		repKnob("rep", repFacts, len(workers), func(i int) string { return workers[i].ws.ID }),
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
