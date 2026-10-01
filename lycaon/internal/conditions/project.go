package conditions

import "github.com/lycaon/lycaon/internal/toolscope"

// RegisterProjectConditions registers chat-first project root vocabulary.
func RegisterProjectConditions(reg *ConditionRegistry) error {
	if reg == nil {
		return nil
	}
	if err := reg.Register("project_has_roots", func(ec EvalContext) (bool, error) {
		return ec.ProjectRootCount > 0, nil
	}); err != nil {
		return err
	}
	return reg.Register("project_has_no_roots", func(ec EvalContext) (bool, error) {
		return ec.ProjectRootCount == 0, nil
	})
}

// RegisterProjectToolConditions registers tool predicates that depend on project roots.
func RegisterProjectToolConditions(reg *ConditionRegistry) error {
	if reg == nil {
		return nil
	}
	// The complement of the catalog's no_folder_allowlist, which the coordinator
	// surface also reads to hide the same tools.
	return reg.Register("tool_requires_project_roots", func(ec EvalContext) (bool, error) {
		return toolscope.RequiresProjectRoots(ec.ToolName), nil
	})
}
