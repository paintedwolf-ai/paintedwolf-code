package wiring

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Every phase transition the host announces with a PhaseAdvanced wake either
// opens a coordinator turn or is held for a typed reason the entered phase
// declares. A wake the loop drops for any other reason fails with the
// session's wake ledger. Cases come from the shipped manifests, so a new phase
// or workflow is covered without listing it here.
func TestEveryAnnouncedPhaseTransitionWakesTheCoordinator(t *testing.T) {
	h := BuildForTest(t, WithRecordingLLM())
	limits := &phaseWakeLimits{}
	h.Sessions.Manager.Limits.SetProvider(limits)
	gates := &armedPhaseGates{next: h.Workflows.Manager.Phases.Gates}
	h.Workflows.Manager.Phases.Gates = gates
	all := startableShippedManifests(t)
	for _, m := range all {
		h.RegisterManifest(m)
	}
	ctx := h.OwnerCtx(t, context.Background())
	cases := 0
	for _, key := range sortedKeys(all) {
		m := all[key]
		for _, edge := range announcedPhaseEdges(m) {
			cases++
			t.Run(key+"/"+edge.String(), func(t *testing.T) {
				d := &phaseWakeDriver{h: h, ctx: ctx, limits: limits, gates: gates, catalog: all, manifest: m}
				d.check(t, edge, cases)
			})
		}
	}
	if cases == 0 {
		t.Fatal("no shipped workflow announces a phase transition")
	}
	if n := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop().Nudges.Outcomes.Violations(); n != 0 {
		t.Fatalf("%d host wakes were decided twice or left without an outcome", n)
	}
}

// Fixture preparation walks runs with the coordinator loop off, so only the
// transition under test can open a turn.
type phaseWakeLimits struct{ enabled atomic.Bool }

func (l *phaseWakeLimits) SessionLimits(string) settings.SessionLimits {
	limits := settings.DefaultSessionLimits()
	enabled := l.enabled.Load()
	limits.CoordinatorLoop = &enabled
	return limits
}

// armedPhaseGates reports the gates of one run's phase as met while armed.
// Gate conditions have their own contracts; this invariant starts once the
// host commits a transition.
type armedPhaseGates struct {
	next  workflowgates.GateEvaluator
	mu    sync.Mutex
	runID string
	phase string
}

func (g *armedPhaseGates) PhaseGateMet(ctx context.Context, m workflowdef.Manifest, run *wire.WorkflowRun, vars map[string]any) (bool, workflowgates.GateCheckResult, error) {
	g.mu.Lock()
	armed := run != nil && run.ID == g.runID && run.CurrentPhase == g.phase
	g.mu.Unlock()
	if armed {
		return true, workflowgates.GateCheckResult{}, nil
	}
	return g.next.PhaseGateMet(ctx, m, run, vars)
}

func (g *armedPhaseGates) arm(runID, phase string) func() {
	g.mu.Lock()
	g.runID, g.phase = runID, phase
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.runID, g.phase = "", ""
		g.mu.Unlock()
	}
}

type phaseWakeDriver struct {
	h        *Harness
	ctx      context.Context
	limits   *phaseWakeLimits
	gates    *armedPhaseGates
	catalog  map[string]workflowdef.Manifest
	manifest workflowdef.Manifest
	sess     *wire.Session
	dir      string
}

func (d *phaseWakeDriver) check(t *testing.T, edge phaseEdge, n int) {
	d.limits.enabled.Store(false)
	run := d.start(t, n)
	path, ok := pathToPhase(d.manifest, run.CurrentPhase, edge.from)
	if !ok {
		t.Fatalf("no announced path from %q to %q", run.CurrentPhase, edge.from)
	}
	for _, step := range path {
		run = d.move(t, run, step)
	}
	d.prepare(t, run, edge)

	loop := d.h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop()
	requests := len(d.h.Recording.AllRequests())
	holds := declaredHolds(d.catalog, d.manifest, edge.to)
	step := fmt.Sprintf("the %s wake", edge)
	watch := WatchWakes(t, d.h.ServeApp, d.sess.ID)
	seen := lastWakeSeq(loop.Nudges.Outcomes.Ledger(d.sess.ID))
	d.limits.enabled.Store(true)
	d.move(t, run, edge)

	// A declared hold parks the wake in admission, or the host parks the
	// session without announcing one. Either way the hold must be in effect.
	if allow, reason, err := loop.Admission.ShouldLoopWake(d.ctx, d.sess.ID, anchor.PhaseAdvanced); err == nil && !allow && holds[reason] {
		for _, outcome := range loop.Nudges.Outcomes.Ledger(d.sess.ID) {
			if outcome.Seq <= seen || outcome.Turn != "" || outcome.Wake != anchor.PhaseAdvanced {
				continue
			}
			if outcome.Result != loopwake.WakeDelivered && (outcome.Result != loopwake.WakeParked || outcome.Reason != "denied") {
				t.Fatalf("%s under declared hold %s: %s %s: %s\n%s", step, reason, outcome.Wake, outcome.Result, outcome.Reason, watch.Report())
			}
		}
		return
	}
	watch.AwaitCompletedTurn(t, step, anchor.PhaseAdvanced)
	if len(d.h.Recording.AllRequests()) <= requests {
		t.Fatalf("%s completed a turn without a model request\n%s", step, watch.Report())
	}
}

func lastWakeSeq(ledger []loopwake.WakeOutcome) uint64 {
	var seq uint64
	for _, outcome := range ledger {
		seq = max(seq, outcome.Seq)
	}
	return seq
}

func (d *phaseWakeDriver) start(t *testing.T, n int) *wire.WorkflowRun {
	t.Helper()
	d.dir = d.h.ProjectDir(t, fmt.Sprintf("phase-wake-%d", n))
	sess, err := d.h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureSpec}, d.dir)
	testutil.FailErr(t, "create session", err)
	d.sess = sess
	var run *wire.WorkflowRun
	if d.manifest.Attach.Policy == workflowdef.AttachPolicySessionCreate {
		AttachDefaultAmbient(t, d.h, d.ctx, sess.ID)
		run, err = d.h.Workflows.Manager.Store.Runs.ActiveBySession(d.ctx, sess.ID)
		testutil.FailErr(t, "ambient run", err)
	} else {
		run, err = d.h.Workflows.Manager.Starts.StartHuman(d.ctx, sess.ID, wire.StartWorkflowRunRequest{
			WorkflowID: d.manifest.ID, WorkflowVersion: d.manifest.Version, Request: "Change the greeting.",
		})
		testutil.FailErr(t, "start "+d.manifest.ID, err)
	}
	if run == nil || run.WorkflowID != d.manifest.ID {
		t.Fatalf("active run = %+v, want a %s run", run, d.manifest.ID)
	}
	return d.reload(t, run.ID)
}

func (d *phaseWakeDriver) reload(t *testing.T, runID string) *wire.WorkflowRun {
	t.Helper()
	run, err := d.h.Workflows.Manager.Store.Runs.Get(d.ctx, runID)
	testutil.FailErr(t, "get run", err)
	return run
}

// prepare walks an invoked child to its last phase, so completing the child is
// the single step under test.
func (d *phaseWakeDriver) prepare(t *testing.T, run *wire.WorkflowRun, edge phaseEdge) {
	t.Helper()
	if edge.kind != edgeChildComplete {
		return
	}
	child := d.child(t, run)
	manifest, err := d.h.Workflows.Manager.Resolver.ForRun(d.ctx, child)
	testutil.FailErr(t, "child manifest", err)
	for range manifest.PhaseDefs {
		next, ok := manifest.ResolveAdvanceTargetForRun(child, child.CurrentPhase)
		if !ok || terminalPhase(manifest, next) {
			return
		}
		child = d.advance(t, child)
	}
	t.Fatalf("child %s never reached its last phase", child.WorkflowID)
}

func (d *phaseWakeDriver) child(t *testing.T, parent *wire.WorkflowRun) *wire.WorkflowRun {
	t.Helper()
	runs, err := d.h.Workflows.Manager.Store.Runs.ListBySession(d.ctx, d.sess.ID, 100, nil)
	testutil.FailErr(t, "list runs", err)
	for i := range runs {
		if runs[i].ParentRunID != nil && *runs[i].ParentRunID == parent.ID && runs[i].Status == wire.WorkflowRunStatusRunning {
			return &runs[i]
		}
	}
	t.Fatalf("phase %s of %s invoked no running child", parent.CurrentPhase, parent.WorkflowID)
	return nil
}

// move commits one transition through the host path that announces it.
func (d *phaseWakeDriver) move(t *testing.T, run *wire.WorkflowRun, edge phaseEdge) *wire.WorkflowRun {
	t.Helper()
	if run.CurrentPhase != edge.from {
		t.Fatalf("run is at %q, want %q before %s", run.CurrentPhase, edge.from, edge)
	}
	mgr := d.h.Workflows.Manager
	var err error
	switch edge.kind {
	case edgeAutoAdvance:
		return d.advance(t, run)
	case edgeHumanApproval:
		d.approve(t, run)
	case edgeUserInput:
		if edge.choice != "" {
			_, err = mgr.Feedback.ResolveUserDecision(d.ctx, d.sess.ID, run.ID, edge.from, []string{edge.choice}, "")
		} else {
			_, err = mgr.Feedback.ResolveUserFeedback(d.ctx, d.sess.ID, run.ID, edge.from, "Use the defaults.")
		}
	case edgeChildComplete:
		d.advance(t, d.child(t, run))
	case edgeReviewLoop:
		// The verdict's validation has its own contracts; this drives the
		// announcement the host makes once a verdict keeps the loop open.
		mgr.Verdicts.OnReviewLoopHeld(d.ctx, d.sess.ID, edge.decisionRequired)
	case edgeTransition:
		_, err = mgr.Phases.FireTransition(d.ctx, run.ID, edge.transitionID, edge.actor)
	}
	testutil.FailErr(t, edge.String(), err)
	run = d.reload(t, run.ID)
	if run.CurrentPhase != edge.to {
		t.Fatalf("after %s the run is at %q (status %s)", edge, run.CurrentPhase, run.Status)
	}
	return run
}

func (d *phaseWakeDriver) advance(t *testing.T, run *wire.WorkflowRun) *wire.WorkflowRun {
	t.Helper()
	disarm := d.gates.arm(run.ID, run.CurrentPhase)
	defer disarm()
	advanced, err := d.h.Workflows.Manager.Phases.Advance(d.ctx, run.ID)
	testutil.FailErr(t, "advance "+run.WorkflowID+" from "+run.CurrentPhase, err)
	return advanced
}

// approve issues the human approval through the blueprint approval path. The
// approval's readiness conditions have their own contracts, so they read met.
func (d *phaseWakeDriver) approve(t *testing.T, run *wire.WorkflowRun) {
	t.Helper()
	approvals := d.h.Workflows.Manager.Approvals
	def, _ := d.manifest.PhaseByID(run.CurrentPhase)
	ready := conditions.NewRegistry()
	met := func(conditions.EvalContext) (bool, error) { return true, nil }
	for _, name := range []string{"blueprint_materialized", strings.TrimSpace(def.HumanApproval.Readiness)} {
		if name != "" {
			testutil.FailErr(t, "register readiness "+name, ready.Register(name, met))
		}
	}
	original := approvals.Registry
	approvals.Registry = ready
	defer func() { approvals.Registry = original }()
	bp, err := d.h.Workflows.Blueprints.Get(d.ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "get blueprint", err)
	_, err = approvals.ApprovePlan(d.ctx, run.ProjectID, run.BlueprintPath, d.dir, run.ID, run.Revision, workflowdef.HashBlueprintContent(bp.Content))
	testutil.FailErr(t, "approve "+run.CurrentPhase, err)
}
