package progress

import api "github.com/lycaon/lycaon/pkg/api"

// DiffSteps compares visible rows across snapshots by their stable row IDs.
func DiffSteps(before, after Snapshot) []api.ProgressChange {
	prevVisible := visibleRows(before.Rows)
	nextVisible := visibleRows(after.Rows)

	prevByID := make(map[string]Row, len(prevVisible))
	for _, r := range prevVisible {
		if r.ID != "" {
			prevByID[r.ID] = r
		}
	}
	nextIDs := make(map[string]struct{}, len(nextVisible))
	for _, r := range nextVisible {
		if r.ID != "" {
			nextIDs[r.ID] = struct{}{}
		}
	}

	var changes []api.ProgressChange
	for _, n := range nextVisible {
		nLabel := clampLabel(n.Label)
		if n.ID != "" {
			if p, ok := prevByID[n.ID]; ok {
				pLabel := clampLabel(p.Label)
				if p.State != n.State {
					changes = append(changes, api.ProgressChange{
						Kind:  transitionKind(n.State),
						Label: nLabel,
						State: n.State,
					})
				} else if pLabel != nLabel {
					changes = append(changes, api.ProgressChange{
						Kind:      api.ProgressChangeUpdated,
						Label:     nLabel,
						PrevLabel: pLabel,
						State:     n.State,
					})
				}
				continue
			}
		}
		changes = append(changes, api.ProgressChange{
			Kind:  api.ProgressChangeCreated,
			Label: nLabel,
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
			Label: clampLabel(p.Label),
			State: p.State,
		})
	}
	return changes
}

func visibleRows(rows []Row) []Row {
	var out []Row
	for _, r := range rows {
		if r.State == ProgressStateOptional {
			continue
		}
		out = append(out, r)
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
