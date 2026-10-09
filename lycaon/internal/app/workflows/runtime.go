package workflows

import (
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/rules"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow"
)

// Runtime holds the wired workflow state, definitions, composition, and rules engines.
type Runtime struct {
	Store         *runstate.Repository
	Drafts        *workflowdrafts.SQL
	Manifests     *workflowdef.Registry
	Resolver      workflowcatalog.Resolver
	Manager       *workflow.RunManager
	Composer      *workflowcomposition.Composer
	Persister     *workflowcomposition.Persister
	Conditions    *conditions.ConditionRegistry
	Rules         *rules.PostureRuleEngine
	ProjectRules  *rules.ProjectRulesOverlay
	BundledRules  map[string]*rules.RulesConfig
	Blueprints    *blueprint.Manager
	Evidence      inspector.EvidenceStore
	Inspector     *inspector.SimpleInspector
}
