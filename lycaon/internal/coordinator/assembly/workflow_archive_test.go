package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const sealedSurveyArchive = "security-survey/1.0.0"

// archiveManifests maps bound session IDs to their run's archive key.
type archiveManifests map[string]string

func (m archiveManifests) ActiveManifest(_ context.Context, sessionID string) (ActiveWorkflowManifest, bool) {
	archive, ok := m[sessionID]
	return ActiveWorkflowManifest{Archive: archive}, ok
}

func archiveTestEngine(t *testing.T) *AssemblyEngine {
	t.Helper()
	return boundArchiveEngine(t, archiveManifests{"sealed": sealedSurveyArchive, "live": ""})
}

func boundArchiveEngine(t *testing.T, bound archiveManifests) *AssemblyEngine {
	t.Helper()
	eff := extpackstest.StockCatalog(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Catalog: eff})
	gates, err := feedback.LoadGateFeedbackCatalogWithCatalog(eff)
	testutil.FailErr(t, "load gate feedback", err)
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		Prompts:                 pe,
		Injects:                 prompts.NewInjectRenderer(pe),
		GateFeedback:            gates,
		ProjectOverlayRootPaths: func(context.Context, *api.Session) []string { return nil },
		Workflows:               bound,
	})
	return engine
}

// A run on a retired version renders the guidance sealed with that version.
func TestRetiredRunReadsSealedPromptGuidance(t *testing.T) {
	prompt := testPromptSurface(archiveTestEngine(t))
	render := func(sessionID string) (string, string) {
		pe, revision, err := prompt.projectPromptsSnapshot(t.Context(), &api.Session{ID: sessionID, WorkspacePath: t.TempDir()})
		testutil.FailErr(t, "snapshot "+sessionID+" prompts", err)
		out, err := pe.Render(t.Context(), "guidance/coordinator-security-plan.md", map[string]any{})
		testutil.FailErr(t, "render "+sessionID+" guidance", err)
		return out, revision
	}
	sealed, sealedRevision := render("sealed")
	live, liveRevision := render("live")
	if sealed == live {
		t.Fatal("retired run rendered the live guidance")
	}
	if sealedRevision == liveRevision {
		t.Fatal("retired and live runs share a prompt revision")
	}
}

// A run on a retired version is held to the gate feedback sealed with it.
func TestRetiredRunReadsSealedGateFeedback(t *testing.T) {
	turn := testTurnContext(archiveTestEngine(t))
	frame := inject.CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{WorkflowID: "security-survey", RunID: "run-1", CurrentPhase: "challenge"},
		Runtime: inject.WorkflowRuntimeSnapshot{Phases: []inject.WorkflowPhaseRow{{
			ID: "challenge", Gates: []inject.WorkflowGateState{{ID: "evidence_passed:survey_challenged"}},
		}}},
		Roster: &inject.AgentRoster{},
	}
	render := func(sessionID string) string {
		msgs, err := turn.prependCoordinatorRunInject(t.Context(), &api.Session{ID: sessionID, WorkspacePath: t.TempDir()},
			frame, nil, &TurnAssemblyScratch{}, nil)
		testutil.FailErr(t, "render "+sessionID+" run inject", err)
		var joined strings.Builder
		for _, msg := range msgs {
			joined.WriteString(msg.Content)
		}
		return joined.String()
	}
	// The live definition admits NEEDS_INVESTIGATION; 1.0.0 predates it.
	if live := render("live"); !strings.Contains(live, "NEEDS_INVESTIGATION") {
		t.Fatalf("live run lost the current gate feedback:\n%s", live)
	}
	if sealed := render("sealed"); strings.Contains(sealed, "NEEDS_INVESTIGATION") {
		t.Fatalf("retired run read the live gate feedback:\n%s", sealed)
	}
}

// AC-COMPAT-04, MC-INV-17: a run pinned to each sealed version renders its own
// guidance and runs on the surfaces of this host, with no retired publication
// protocol reaching the model.
func TestEverySealedVersionRendersOnThisHost(t *testing.T) {
	eff := extpackstest.StockCatalog(t)
	manifests, _, err := workflowdef.LoadManifestsFromCatalog(eff)
	testutil.FailErr(t, "load manifests", err)
	surfaces, err := surfacecatalog.Load()
	testutil.FailErr(t, "load surfaces", err)
	stems := map[string][]string{}
	for _, unitID := range eff.LoadedUnitIDs() {
		key, rest, ok := extpacks.SplitArchiveUnitID(unitID)
		if stem, isGuidance := strings.CutPrefix(rest, "guidance/"); ok && isGuidance && !strings.Contains(stem, "/") {
			stems[key] = append(stems[key], stem)
		}
	}
	sealed := 0
	for _, manifest := range manifests {
		if !manifest.Retired {
			continue
		}
		sealed++
		key := extpacks.ArchiveKey(manifest.ID, manifest.Version)
		t.Run(key, func(t *testing.T) {
			for _, phase := range manifest.PhaseDefs {
				if phase.CoordinatorSurface == "" {
					continue
				}
				if _, err := surfaces.Surface(phase.CoordinatorSurface); err != nil {
					t.Fatalf("phase %s names surface %q this host lacks: %v", phase.ID, phase.CoordinatorSurface, err)
				}
			}
			prompt := testPromptSurface(boundArchiveEngine(t, archiveManifests{"pinned": key}))
			pe, _, err := prompt.projectPromptsSnapshot(t.Context(), &api.Session{ID: "pinned", WorkspacePath: t.TempDir()})
			testutil.FailErr(t, "snapshot prompts", err)
			for _, stem := range stems[key] {
				out, err := pe.Render(t.Context(), "guidance/"+stem, map[string]any{})
				testutil.FailErr(t, "render "+stem, err)
				if strings.TrimSpace(out) == "" {
					t.Fatalf("%s rendered empty", stem)
				}
				for _, retired := range []string{"surface_note", "⟦D", "completion_report"} {
					if strings.Contains(out, retired) {
						t.Fatalf("%s teaches %q", stem, retired)
					}
				}
			}
		})
	}
	if sealed < 3 {
		t.Fatalf("found %d sealed versions, want the three in the pack archives", sealed)
	}
}
