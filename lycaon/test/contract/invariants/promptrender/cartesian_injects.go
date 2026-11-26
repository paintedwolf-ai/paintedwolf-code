package promptrender

import (
	"context"
	"fmt"
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
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/internal/workerroster"
	"github.com/lycaon/lycaon/internal/workerstate"
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

	if err := runAllKicksAndNudges(ctx, s, cReps, wReps, sum); err != nil {
		return err
	}
	if err := runCoordinatorInjectRenders(ctx, s, cReps, resolver, sum); err != nil {
		return err
	}
	if err := runWorkerInjectRenders(ctx, s, wReps, sum); err != nil {
		return err
	}
	if err := runGuidanceAndUtilityRenders(ctx, s, cReps, wReps, sum); err != nil {
		return err
	}
	return runGateFeedbackRenders(ctx, s, sum)
}

func runAllKicksAndNudges(
	ctx context.Context,
	s *Stock,
	cReps []coordRep,
	wReps []workerRep,
	sum *CartesianSummary,
) error {
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		return err
	}
	wfReg, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return err
	}
	// Kick renders share no keys with the other families; a sweep-local cache
	// is released once the sweep ends.
	sweep := newKickSweep(s, hoststate.NewRenderCache(), func(v string) { sum.Violations = append(sum.Violations, v) })
	if err := sweep.coordinatorKicks(ctx, anchorReg, informKickIDs(anchorReg, "coordinator-", "session-agent-enabled"), cReps); err != nil {
		return err
	}
	if err := sweep.workerKicks(ctx, anchorReg, informKickIDs(anchorReg, "worker-"), wReps); err != nil {
		return err
	}
	pairs, err := cachedCoordinatorPairs()
	if err != nil {
		return err
	}
	if err := sweep.phaseKicks(ctx, wfReg, anchorReg, pairs); err != nil {
		return err
	}
	sum.KickAndNudgeCount += sweep.rendered
	return nil
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

	firstOfReader := map[int]bool{}
	for _, i := range distinctReaderIndexes(len(cReps), func(i int) prompts.Reader { return cReps[i].auth.Reader }) {
		firstOfReader[i] = true
	}
	for idx, cr := range cReps {
		resolveNamed := resolver.forCoordinatorState(cr.state)

		// command-jobs: running, exited and held; exited only; running only.
		jobSets := [][]bgprocess.JobSnapshot{jobsBoth, jobsBoth[1:], jobsBoth[:1]}
		// Live jobs and held calls project their control tools onto the turn.
		jobAuths := map[bool]hoststate.TurnAuthority{}
		for jIdx, jobs := range jobSets {
			var held []heldcall.Running
			if jIdx == 0 {
				held = heldRunning
			} else if !firstOfReader[idx] {
				continue
			}
			jobAuth, ok := jobAuths[len(held) > 0]
			if !ok {
				a, err := hoststate.FreezeTurnAuthorityWithResources(ctx, cr.state, toolcontract.ResourcePresence{CommandJobs: true, HeldCalls: len(held) > 0})
				if err != nil {
					return err
				}
				jobAuth, jobAuths[len(held) > 0] = a, a
			}
			cmdBlock := inject.RenderCommandJobsBlock(ctx, injRenderer, jobAuth.Reader, jobAuth.Schemas["wait"], "sess-inj", jobs, held, now, "coordinator")
			if cmdBlock != "" {
				sum.InjectAndGuidanceCount++
				viols := checkClaimsOnce(s, "inject/command-jobs ("+cr.state.ID+")", cmdBlock, jobAuth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}

		// turn-state (rich workers and minimal).
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
			{
				Reader: cr.auth.Reader, WorkersOmitted: 0,
				Workers: workerroster.Roster{Rows: []workerroster.Row{{Name: "w-only", Agent: "implementer", State: workerstate.Working}}}.Vars(),
			},
			{
				Reader: cr.auth.Reader, WorkersOmitted: 1,
				Workers: workerroster.Roster{Rows: []workerroster.Row{{
					Name: "w-solo", Agent: "implementer", State: workerstate.Working, About: "Second round",
					Result:  &workerroster.Result{Handle: "res-2", Disposition: "open"},
					Changed: workerroster.Paths{Shown: []string{"a.go"}},
					Read:    workerroster.Paths{Shown: []string{"a.go"}},
				}}}.Vars(),
			},
		}
		for tIdx, tsIn := range tsInputs {
			if tIdx > 0 && !firstOfReader[idx] {
				continue
			}
			tsBlock, err := inject.RenderTurnStateBlock(ctx, injRenderer, "sess-ts", tsIn)
			if err != nil {
				return err
			}
			sum.InjectAndGuidanceCount++
			if tsBlock != "" {
				viols := checkClaimsOnce(s, "inject/turn-state ("+cr.state.ID+")", tsBlock, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}

		// board-orientation, source-changes, implement-spawn and transition,
		// once per distinct reader.
		if firstOfReader[idx] {
			if err := renderRemainingCoordInjectsForRep(ctx, s, injRenderer, cr, resolveNamed, now, idx == 0, sum); err != nil {
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
	first bool,
	sum *CartesianSummary,
) error {
	namedWorkers := make([]api.BoardWorker, packboard.MaxOrientationWorkers+2)
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
	for _, sb := range snaps {
		bText, err := inject.RenderBoardOrientationInject(ctx, injRenderer, cr.auth.Reader, "sess-b", sb.snap, packboard.InjectScopeFull, false, sb.legend, now)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		if bText != "" {
			viols := checkClaimsOnce(s, "inject/board-orientation ("+cr.state.ID+")", bText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	// The inventory_at branch has no producer input in the snapshot renderer.
	if first {
		invText, err := injRenderer.RenderWithReader(ctx, cr.auth.Reader, "board-orientation", map[string]any{
			"pack_sentinel": inject.BoardOrientationInjectSentinel,
			"inventory_at":  "14:00 UTC",
			"lines":         []map[string]any{{"text": "Inventory fresh."}},
		})
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		viols := checkClaimsOnce(s, "inject/board-orientation-inv", invText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
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
			{
				OtherFiles: 2, OtherEffects: 2, OtherWorkingSet: true,
				Files: []inject.SourceChangeFile{{Path: "seen.go", Actor: "user", Op: "modify", At: "14:00 UTC", Effects: 1, WorkingSet: true}},
			},
		}
		for _, br := range scBriefs {
			scText := inject.RenderSourceChangesBlock(ctx, injRenderer, cr.auth.Reader, "sess-sc", br)
			if scText != "" {
				sum.InjectAndGuidanceCount++
				viols := checkClaimsOnce(s, "inject/source-changes ("+cr.state.ID+")", scText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
				sum.Violations = append(sum.Violations, viols...)
			}
		}
	}

	// implement-spawn (repoKnownEmpty false & true)
	for _, emptyRepo := range []bool{false, true} {
		spText, err := inject.RenderImplementSpawnInject(ctx, injRenderer, "sess-sp", cr.state.SpawnAllowlist, spawn.MaxInFlightTaskWorkers, cr.auth.Reader, emptyRepo)
		if err != nil {
			return err
		}
		if spText != "" {
			sum.InjectAndGuidanceCount++
			viols := checkClaimsOnce(s, "inject/implement-spawn ("+cr.state.ID+")", spText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}

	for _, tr := range modeTransitionsInto(cr.state) {
		occ := map[string]any{}
		if err := prompts.MergeCoordinatorPromptVars(cr.state.TurnProfile.SurfaceID, prompts.ExecutionModePromptTransition{
			ExecutionModePrevious: tr.ExecutionModePrevious,
			ExecutionModeEntered:  tr.ExecutionModeEntered,
			ExecutionModeLeft:     tr.ExecutionModeLeft,
		}, "", occ); err != nil {
			return err
		}
		trText, err := anchor.RenderInformWithReader(ctx, anchor.InjectTransition, anchor.MatchContext{Surface: "coordinator", SessionID: "sess-tr"}, injRenderer, cr.auth.Reader, occ)
		if err != nil {
			return err
		}
		sum.InjectAndGuidanceCount++
		if strings.TrimSpace(trText) != "" {
			viols := checkClaimsOnce(s, "inject/transition ("+cr.state.ID+")", trText, cr.auth, cr.state.SessionMode, cr.state.Posture, toolvocabtest.SubjectSelfNow(), nil, nil, resolveNamed, true)
			sum.Violations = append(sum.Violations, viols...)
		}
	}
	return nil
}

// modeTransitionsInto computes, for st's surface, the transition the host
// injects after each prior execution-mode family, and on an active run after
// each mode a phase entry can force.
func modeTransitionsInto(st hoststate.CoordinatorState) []surface.TransitionVars {
	current := surface.ExecutionModeFamily(st.TurnProfile.SurfaceID)
	families := []string{"", surface.ExecutionModeFamilyInvestigate, surface.ExecutionModeFamilyOrchestrate, surface.ExecutionModeFamilyWrapup}
	causes := [][]surface.ModeTransitionCause{nil}
	if st.RunContext.RunID != "" {
		for _, mode := range families {
			if workflowdef.IsForceExecutionMode(mode) {
				causes = append(causes, []surface.ModeTransitionCause{{Kind: surface.ModeTransitionCausePhaseHook, Mode: mode}})
			}
		}
	}
	var out []surface.TransitionVars
	for _, previous := range families {
		for _, c := range causes {
			if tr := surface.ComputeModeTransition(previous, current, c); inject.ShouldRenderTransitionInject(tr) {
				out = append(out, tr)
			}
		}
	}
	return out
}
