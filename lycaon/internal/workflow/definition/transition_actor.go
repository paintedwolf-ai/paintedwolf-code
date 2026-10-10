package definition

func TransitionActorAllowed(edge PhaseTransitionDef, actor string) bool {
	for _, a := range edge.Actors {
		if a == actor {
			return true
		}
	}
	return false
}
