package wiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

type replayFixture struct {
	WorkflowID      string        `yaml:"workflow_id"`
	WorkflowVersion string        `yaml:"workflow_version"`
	Phases          []replayPhase `yaml:"phases"`
	TerminalStatus  string        `yaml:"terminal_status"`
}

type replayPhase struct {
	Phase          string             `yaml:"phase"`
	ExpectedPrompt string             `yaml:"expected_prompt"`
	Submissions    []replaySubmission `yaml:"submissions"`
	ReviewerAgents []string           `yaml:"reviewer_agents"`
	GateOutcomes   map[string]bool    `yaml:"gate_outcomes"`
}

type replaySubmission struct {
	Verdict         map[string]any `yaml:"verdict"`
	ExpectRejection bool           `yaml:"expect_rejection"`
	RejectionReason string         `yaml:"rejection_reason"`
}

type changesDoc struct {
	Changes []changeEntry `yaml:"changes"`
}

type changeEntry struct {
	ID          string `yaml:"id"`
	Category    string `yaml:"category"`
	Description string `yaml:"description"`
}

// TestArchivedWorkflowReplay_100 verifies that the sealed 1.0.0 security-survey
// faithfully executes against the current Go runtime using author-specified replay
// fixtures: exact archived prompt bindings, real submit_verdict semantics under
// the sealed verdict schemas (including enum rejection), reviewer-roster
// enforcement, and host-driven phase advance. Output drift fails unless
// explicitly documented in changes.yaml with spec_fix or safety.
func TestArchivedWorkflowReplay_100(t *testing.T) {
	fixtureDir := filepath.Join("testdata", "replay", "security-survey@1.0.0")
	replayPath := filepath.Join(fixtureDir, "replay.yaml")
	data, err := os.ReadFile(replayPath)
	testutil.FailErr(t, "read replay.yaml", err)

	var fixture replayFixture
	err = yaml.Unmarshal(data, &fixture)
	testutil.FailErr(t, "unmarshal replay.yaml", err)

	changesPath := filepath.Join(fixtureDir, "changes.yaml")
	if changeData, err := os.ReadFile(changesPath); err == nil {
		var doc changesDoc
		if err := yaml.Unmarshal(changeData, &doc); err != nil {
			t.Fatalf("unmarshal changes.yaml: %v", err)
		}
		for _, c := range doc.Changes {
			if c.Category != "spec_fix" && c.Category != "safety" {
				t.Fatalf("change %s category %q invalid: only spec_fix and safety permitted", c.ID, c.Category)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading changes.yaml: %v", err)
	}

	h := BuildForTest(t, WithAutoCompleteDelegation(), WithoutCoordinatorLoop())
	ctx := context.Background()
	dir := h.ProjectDir(t, fixture.WorkflowID)
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, dir)
	testutil.FailErr(t, "create session", err)

	// Claims-ground citations resolve against a seeded survey record, exactly as
	// a live 1.0.0 run would have observed the digested worker evidence.
	testutil.FailErr(t, "seed survey evidence", h.Store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "survey#1", Path: "internal/store/query.go",
		Kind: "read", Shape: evidence.ShapeFileRegion, SourceTool: "read", Fidelity: evidence.FidelityStructured,
		LineRanges: []evidence.LineRange{{Start: 88, End: 88}},
		Body:       []string{"id reaches Sprintf in query.go:88"},
	}))

	now := time.Now().UTC()
	runID := "run-replay-100-" + uuid.NewString()
	run := &api.WorkflowRun{
		ID:              runID,
		SessionID:       sess.ID,
		WorkflowID:      fixture.WorkflowID,
		WorkflowVersion: fixture.WorkflowVersion,
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    fixture.Phases[0].Phase,
		CreatedAt:       now,
		UpdatedAt:       now,
		Revision:        1,
	}
	err = h.WorkflowMgr.Store.State.CreateState(ctx, run, dir, map[string]any{})
	testutil.FailErr(t, "create state for 1.0.0 replay run", err)

	// Manifest resolution must resolve exact archived version.
	manifest, err := h.WorkflowMgr.Resolver.ForRunID(ctx, runID)
	testutil.FailErr(t, "ManifestForRunID", err)
	if manifest.Version != fixture.WorkflowVersion {
		t.Fatalf("expected manifest version %s, got %s", fixture.WorkflowVersion, manifest.Version)
	}
	if active, ok := h.WorkflowMgr.Policy.ActiveManifest(ctx, sess.ID); !ok || !manifest.Retired || active.Archive != "security-survey/"+fixture.WorkflowVersion {
		t.Fatalf("expected the run to resolve its sealed archive, got retired=%v active=%+v", manifest.Retired, active)
	}

	for i, p := range fixture.Phases {
		// 1. Exact prompt matching for the phase.
		matchCtx := anchor.RunMatch(runSource(run), "phase", p.Phase)
		binding, err := anchor.RegistryFor(ctx, sess.ID).ResolveInform(anchor.PhaseEntered, matchCtx)
		testutil.FailErr(t, "ResolveInform for phase "+p.Phase, err)
		if binding == nil {
			t.Fatalf("phase %s has no matching prompt binding", p.Phase)
		}
		if binding.Render != p.ExpectedPrompt {
			t.Fatalf("phase %s expected prompt %q, got %q", p.Phase, p.ExpectedPrompt, binding.Render)
		}

		// 2. Review phases replay through real verdict submissions; the
		// terminal verdict itself satisfies the evidence gate and advances.
		if len(p.Submissions) > 0 || len(p.ReviewerAgents) > 0 {
			var citations []api.CitationGroundingCitedEvidence
			if len(p.ReviewerAgents) > 0 {
				// Stamp the phase-enter reviewer roster the live host would
				// have recorded, then land each reviewer's succeeded envelope.
				run, err = h.WorkflowMgr.Store.Runs.Get(ctx, runID)
				testutil.FailErr(t, "get run for roster in "+p.Phase, err)
				vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, runID)
				testutil.FailErr(t, "GetScaffoldVars roster in "+p.Phase, err)
				vars = runstate.StampReviewIfSpawnable(vars, p.Phase, p.ReviewerAgents)
				testutil.FailErr(t, "UpdateVars roster in "+p.Phase, h.WorkflowMgr.Store.State.UpdateVars(ctx, run, dir, vars))
				for _, agent := range p.ReviewerAgents {
					child := appendSucceededReviewAgent(t, h, ctx, sess, agent, "")
					citations = append(citations, reviewerCitation(child, agent))
				}
			}
			for _, sub := range p.Submissions {
				verdict := make(map[string]string, len(sub.Verdict))
				for k, v := range sub.Verdict {
					s, ok := v.(string)
					if !ok {
						t.Fatalf("phase %s verdict field %q: want string, got %T", p.Phase, k, v)
					}
					verdict[k] = s
				}
				out, err := h.WorkflowMgr.Verdicts.RecordReviewLoopVerdict(ctx, sess.ID, verdict, citations, nil)
				testutil.FailErr(t, "RecordReviewLoopVerdict in "+p.Phase, err)
				if sub.ExpectRejection {
					if sub.RejectionReason == "" {
						t.Fatalf("phase %s rejected submission needs a rejection_reason", p.Phase)
					}
					if out.Valid || out.Terminal {
						t.Fatalf("phase %s verdict = %+v, want schema rejection", p.Phase, out)
					}
					continue
				}
				if !out.Valid || !out.Terminal {
					t.Fatalf("phase %s verdict = %+v, want valid terminal outcome", p.Phase, out)
				}
			}
			if i+1 < len(fixture.Phases) {
				waitWorkflowPhase(t, ctx, h.WorkflowMgr, runID, fixture.Phases[i+1].Phase)
				run, err = h.WorkflowMgr.Store.Runs.Get(ctx, runID)
				testutil.FailErr(t, "get run after verdict in "+p.Phase, err)
			}
			continue
		}

		// 3. Non-review phases replay through their declared gates.
		vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, runID)
		testutil.FailErr(t, "GetScaffoldVars in "+p.Phase, err)
		for gate, satisfied := range p.GateOutcomes {
			vars = runstate.SetGateSatisfied(vars, gate, satisfied)
		}
		if p.Phase == "execute" {
			vars["worker_cycle"] = map[string]any{"evaluating": true, "summary_status": "complete"}
		}
		run, err = h.WorkflowMgr.Store.Runs.Get(ctx, runID)
		testutil.FailErr(t, "get run before UpdateVars in "+p.Phase, err)
		err = h.WorkflowMgr.Store.State.UpdateVars(ctx, run, dir, vars)
		testutil.FailErr(t, "UpdateVars in "+p.Phase, err)

		run, err = h.WorkflowMgr.Phases.Advance(ctx, runID)
		testutil.FailErr(t, "Advance from "+p.Phase, err)
	}

	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "get run after replay", err)
	if fixture.TerminalStatus == "complete" && run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("expected run status %s, got %s", api.WorkflowRunStatusComplete, run.Status)
	}
}
