package progress

import api "github.com/lycaon/lycaon/pkg/api"

// DiffSteps matches exact labels, then pairs unmatched rows as relabels.
func DiffSteps(prev, next []api.ProgressStep) []api.ProgressChange {
	prevByLabel := make(map[string][]int, len(prev))
	for i, p := range prev {
		prevByLabel[p.Label] = append(prevByLabel[p.Label], i)
	}
	prevMatched := make([]bool, len(prev))

	var changes []api.ProgressChange
	var newCreations []api.ProgressStep
	for _, n := range next {
		idxs := prevByLabel[n.Label]
		if len(idxs) > 0 {
			pi := idxs[0]
			prevByLabel[n.Label] = idxs[1:]
			prevMatched[pi] = true
			if prev[pi].State != n.State {
				changes = append(changes, api.ProgressChange{
					Kind:  transitionKind(n.State),
					Label: n.Label,
					State: n.State,
				})
			}
			continue
		}
		newCreations = append(newCreations, n)
	}

	var removals []api.ProgressStep
	for i, matched := range prevMatched {
		if !matched {
			removals = append(removals, prev[i])
		}
	}

	// Pair unmatched rows by position to represent a relabel.
	paired := len(newCreations)
	if len(removals) < paired {
		paired = len(removals)
	}
	for i := 0; i < paired; i++ {
		changes = append(changes, api.ProgressChange{
			Kind:      api.ProgressChangeUpdated,
			Label:     newCreations[i].Label,
			PrevLabel: removals[i].Label,
			State:     newCreations[i].State,
		})
	}
	for _, c := range newCreations[paired:] {
		changes = append(changes, api.ProgressChange{
			Kind:  api.ProgressChangeCreated,
			Label: c.Label,
			State: c.State,
		})
	}
	for _, r := range removals[paired:] {
		changes = append(changes, api.ProgressChange{
			Kind:  api.ProgressChangeRemoved,
			Label: r.Label,
			State: r.State,
		})
	}
	return changes
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
