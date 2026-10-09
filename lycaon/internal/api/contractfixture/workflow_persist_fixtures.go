package contractfixture

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func NewPersistTestServer(t *testing.T) (*api.Server, wire.Session, workflow.SessionWorkflowStore) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	store := store.NewMemory()
	projectDir := t.TempDir()
	projReg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), projReg, projectDir)
	testutil.FailErr(t, "reg.Create failed", err)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, projectDir)
	sess, err = store.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(t.Context(), agents)
	sessionStore := workflow.NewMemorySessionWorkflowStore()
	policy, err := workflow.LoadComposePolicy()
	testutil.FailErr(t, "workflow.LoadComposePolicy failed", err)
	templates, err := workflow.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	composer := &workflow.Composer{
		SessionStore: sessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
		Templates:    templates,
	}
	persister := &workflow.Persister{
		SessionStore: sessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
	}
	srv := api.NewServer(RequiredTestDeps(t, api.Dependencies{Core: api.CoreDependencies{
		Store: store, Projects: projReg}, Workflow: api.WorkflowDependencies{
		WorkflowCatalog: workflow.ManifestResolver{
			SessionStore:       sessionStore,
			ProjectTierApplies: func(context.Context, string) bool { return true },
		},
		WorkflowComposer: composer, WorkflowPersister: persister}}), nil, api.TestAPIToken)
	return srv, *sess, sessionStore
}
