package contractfixture

import (
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func NewComposeTestServer(t *testing.T) (*api.Server, wire.Session, *workflowcomposition.Composer) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	projReg := project.NewMemoryRegistry()
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), projReg, dir)
	testutil.FailErr(t, "reg.Create failed", err)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create session in store", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(t.Context(), agents)
	sessionStore := workflow.NewMemorySessionWorkflowStore()
	policy, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	templates, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	composer := &workflowcomposition.Composer{
		SessionStore: sessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
		Templates:    templates,
	}
	srv := api.NewServer(RequiredTestDeps(t, api.Dependencies{Core: api.CoreDependencies{
		Store: store, Projects: projReg}, Workflow: api.WorkflowDependencies{
		WorkflowCatalog:  workflowcatalog.Resolver{SessionStore: sessionStore},
		WorkflowComposer: composer}}), nil, api.TestAPIToken)
	return srv, *sess, composer
}
