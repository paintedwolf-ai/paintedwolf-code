package migrations

import "slices"

// RegisteredRetirements returns a copy of the registered retirements by step ID.
func RegisteredRetirements() map[string][]Retirement {
	out := make(map[string][]Retirement, len(retirements))
	for step, items := range retirements {
		out[step] = slices.Clone(items)
	}
	return out
}
