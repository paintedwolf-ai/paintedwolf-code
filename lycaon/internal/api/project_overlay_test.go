package api

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func newProjectOverlayTestServer(t *testing.T, opts ...testDeps) (*Server, *project.MemoryRegistry) {
	t.Helper()

	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sessionStore := store.NewMemory()
	projects := project.NewMemoryRegistry()
	mock := llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{
			Pattern: "write.*file",
			ToolCalls: []llm.MockToolCall{{
				ID:   "call_write",
				Name: "write",
				Args: map[string]any{"path": "out.txt", "content": "x"},
			}},
			FollowUpText: "written",
		}},
	})
	toolRegistry := tools.NewStubRegistry()
	mgr := session.NewManager(sessionStore, mock, toolRegistry, settings.DefaultSessionLimits())
	mgr.SetToolInvoker(testtool.RegistryInvoker{Registry: toolRegistry}, testtool.RegistryInvoker{Registry: toolRegistry})

	postures, err := session.LoadPostureRegistry()
	testutil.FailErr(t, "load posture registry", err)
	packs, err := rules.LoadBundledRules()
	testutil.FailErr(t, "load bundled rules", err)
	conditionRegistry, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	testutil.FailErr(t, "register rule conditions", rules.RegisterRuleConditions(conditionRegistry))
	engine, err := rules.NewPostureRuleEngine(postures, packs, conditionRegistry)
	testutil.FailErr(t, "build posture rule engine", err)
	overlay := rules.NewProjectRulesOverlay(conditionRegistry)
	engine.Overlay = overlay
	mgr.SetPostureRegistry(postures)
	mgr.SetRuleEngine(engine)
	mgr.SetProjectRegistry(projects)

	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
	testutil.FailErr(t, "build trust surfaces store", err)
	engine.ProjectSettingsApply = (&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceProjectSettings,
		Surfaces: surfaces,
		Projects: projects,
	}).Applies

	gate := project.NewMutationGate()
	mgr.SetMutationGate(gate)
	deps := Dependencies{
		Store: sessionStore, Projects: projects, Sessions: mgr, Settings: &settings.Service{TrustSurfaces: surfaces},
		MutationGate: gate, ProjectRules: overlay,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	srv := NewServer(requiredTestDeps(t, deps), nil, TestAPIToken)
	mgr.SetPromotionHook(srv.Project.TryRunPromotion)
	return srv, projects
}
