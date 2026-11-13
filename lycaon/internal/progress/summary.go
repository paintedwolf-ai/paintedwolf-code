package progress

// Summary separates terminal decisions from completed work.
type Summary struct {
	Pending                   int  `json:"pending"`
	Completed                 int  `json:"completed"`
	NotPursued                int  `json:"not_pursued"`
	Optional                  int  `json:"optional"`
	AllRequiredItemsTerminal  bool `json:"all_required_items_terminal"`
	AllRequiredItemsCompleted bool `json:"all_required_items_completed"`
}

func Summarize(content string) Summary {
	var summary Summary
	items := ChecklistItems(content)
	for _, item := range items {
		switch item.State {
		case ProgressStatePending:
			summary.Pending++
		case ProgressStateDone:
			summary.Completed++
		case ProgressStateNA:
			summary.NotPursued++
		case ProgressStateOptional:
			summary.Optional++
		}
	}
	summary.AllRequiredItemsTerminal = len(items) > 0 && summary.Pending == 0
	summary.AllRequiredItemsCompleted = summary.Completed > 0 && summary.Pending == 0 && summary.NotPursued == 0
	return summary
}
