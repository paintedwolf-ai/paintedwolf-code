package contractfixture

import (
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func NewProjectOverlayTestServer(t *testing.T, opts ...TestDeps) (*hostapi.Server, *project.MemoryRegistry) {
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
	mgr := session.NewHost(sessionStore, session.Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolRegistry)
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: toolRegistry})

	postures, err := profiles.LoadPostureRegistry()
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
	mgr.Profiles.SetPostureRegistry(postures)
	mgr.Coordinator.Guards.SetRules(engine)
	mgr.SetProjectRegistry(projects)

	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
	testutil.FailErr(t, "build trust surfaces store", err)
	engine.ProjectSettingsApply = (&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceProjectSettings,
		Surfaces: surfaces,
		Projects: projects,
	}).Applies

	gate := project.NewMutationGate()
	mgr.Runner.Execution.SetMutationGate(gate)
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: sessionStore, Projects: projects, Sessions: mgr, Settings: &settings.Service{TrustSurfaces: surfaces},
		MutationGate: gate}, Source: hostapi.SourceDependencies{ProjectRules: overlay}}
	for _, opt := range opts {
		opt(&deps)
	}
	srv := hostapi.NewServer(RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken)
	mgr.Admission.SetPromotion(srv.Admin.Project.Promotion.TryRunPromotion)
	return srv, projects
}

func TrustSettingsServer(t *testing.T, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	svc, err := settings.NewService()
	testutil.FailErr(t, "settings.NewService", err)

	return NewServerForTest(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store.NewMemory(), Projects: project.NewMemoryRegistry(), Settings: svc}, Storage: hostapi.StorageDependencies{
		ModuleRoot: configlayout.FindModuleRoot()}}, append([]TestDeps{WithExtensionOwner(t)}, opts...)...)
}

func WriteOverlay(t *testing.T, root, basename, content string) {
	t.Helper()
	dir := filepath.Join(root, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(dir, 0o750))
	testutil.FailErr(t, "write "+basename,
		os.WriteFile(filepath.Join(dir, basename), []byte(content), 0o600))
}
