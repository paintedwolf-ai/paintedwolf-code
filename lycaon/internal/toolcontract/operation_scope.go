package toolcontract

import "fmt"

// Validate keeps project and projectless session coordinates distinct without
// granting either scope ambient host authority.
func (scope OperationScope) Validate() error {
	switch scope.Kind {
	case ProjectOperationScope:
		if scope.ProjectID == "" {
			return fmt.Errorf("project operation scope requires project identity")
		}
	case SessionOperationScope:
		if scope.ProjectID != "" || scope.RootID != "" || scope.BranchID != "" {
			return fmt.Errorf("session operation scope cannot carry project tree identity")
		}
	default:
		return fmt.Errorf("explicit project or session operation scope required")
	}
	return nil
}
