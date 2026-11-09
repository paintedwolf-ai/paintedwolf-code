package prompts

import "slices"

func (turn SurfaceTurn) available(names []string) []string {
	if !turn.Plan.Compiled() {
		return names
	}
	return slices.DeleteFunc(slices.Clone(names), func(name string) bool { return !turn.Plan.Addressable(name) })
}
