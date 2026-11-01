package progress

import api "github.com/lycaon/lycaon/pkg/api"

// DiffSteps compares visible items across immutable snapshots by their stable item IDs.
func DiffSteps(before, after Snapshot) []api.ProgressChange {
	prevVisible := visibleItems(before.Items())
	nextVisible := visibleItems(after.Items())

	prevByID := make(map[string]Item, len(prevVisible))
	for _, item := range prevVisible {
		if item.ID != "" {
			prevByID[item.ID] = item
		}
	}
	nextIDs := make(map[string]struct{}, len(nextVisible))
	for _, item := range nextVisible {
		if item.ID != "" {
			nextIDs[item.ID] = struct{}{}
		}
	}

	var changes []api.ProgressChange
	for _, n := range nextVisible {
		if n.ID != "" {
			if p, ok := prevByID[n.ID]; ok {
				if p.State != n.State {
					changes = append(changes, api.ProgressChange{
						Kind:  transitionKind(n.State),
						Label: n.Label,
						State: n.State,
					})
				} else if p.Label != n.Label {
					changes = append(changes, api.ProgressChange{
						Kind:      api.ProgressChangeUpdated,
						Label:     n.Label,
						PrevLabel: p.Label,
						State:     n.State,
					})
				}
				continue
			}
		}
		changes = append(changes, api.ProgressChange{
			Kind:  api.ProgressChangeCreated,
			Label: n.Label,
			State: n.State,
		})
	}
	for _, p := range prevVisible {
		if p.ID != "" {
			if _, ok := nextIDs[p.ID]; ok {
				continue
			}
		}
		changes = append(changes, api.ProgressChange{
			Kind:  api.ProgressChangeRemoved,
			Label: p.Label,
			State: p.State,
		})
	}
	return changes
}

func visibleItems(items []Item) []Item {
	var out []Item
	for _, item := range items {
		if item.State == ProgressStateOptional {
			continue
		}
		out = append(out, item)
	}
	return out
}

func transitionKind(state string) api.ProgressChangeKind {
	switch state {
	case ProgressStateDone:
		return api.ProgressChangeDone
	case ProgressStateNA:
		return api.ProgressChangeNA
	default:
		return api.ProgressChangeReopened
	}
}
