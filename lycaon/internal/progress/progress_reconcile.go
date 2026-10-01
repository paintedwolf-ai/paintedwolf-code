package progress

// SynthesisReconcileOnly reports whether proposed is a reconcile-only update_progress from
// current on implement_synthesis: close or N/A existing rows only — no new checklist scope.
func SynthesisReconcileOnly(current, proposed string) bool {
	if ProgressMissing(current) {
		return false
	}
	currentItems := ChecklistItems(current)
	proposedItems := ChecklistItems(proposed)
	currentByLabel := make(map[string]string, len(currentItems))
	for _, item := range currentItems {
		currentByLabel[item.Label] = item.State
	}
	proposedByLabel := make(map[string]string, len(proposedItems))
	for _, item := range proposedItems {
		proposedByLabel[item.Label] = item.State
	}
	for _, item := range currentItems {
		if item.State != ProgressStatePending {
			continue
		}
		propState, ok := proposedByLabel[item.Label]
		if !ok {
			return false
		}
		if propState != ProgressStatePending && propState != ProgressStateDone && propState != ProgressStateNA {
			return false
		}
	}
	for _, item := range proposedItems {
		prev, ok := currentByLabel[item.Label]
		if !ok {
			return false
		}
		switch item.State {
		case ProgressStatePending:
			if prev != ProgressStatePending && prev != ProgressStateOptional {
				return false
			}
		case ProgressStateOptional:
			if prev != ProgressStateOptional && prev != ProgressStatePending {
				return false
			}
		case ProgressStateDone, ProgressStateNA:
			continue
		default:
			return false
		}
	}
	return true
}
