package llm

// ModelRole identifies which model_policy slot resolved a selection.
type ModelRole string

const (
	ModelRoleCoordinator ModelRole = "coordinator"
	ModelRoleLite        ModelRole = "lite"
	ModelRolePool        ModelRole = "pool"
)

// ModelSelection is the chosen provider/model pair.
type ModelSelection struct {
	ProviderID string
	Model      string
	Role       ModelRole
	// Fallback marks a selection served by the injected mock or manual client
	// under LYCAON_LLM_MOCK / manual mode, not by the resolved provider.
	Fallback bool
}
