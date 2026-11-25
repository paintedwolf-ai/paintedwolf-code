package rejectinventory

// nodeMemo holds finished producer nodes and the stack of nodes still resolving.
type nodeMemo struct {
	memo   map[string]resolution
	active map[string]int
	lowHit int
}

// node resolves one shared producer node (a field, a field's elements or a
// parameter) once. A node re-entered while it is still resolving contributes
// nothing on that path; a result that depended on such an unfinished ancestor
// is not memoized, because the ancestor's other producers are still missing.
func (s *scan) node(key string, compute func() resolution) resolution {
	if r, ok := s.nodes.memo[key]; ok {
		return r
	}
	if at, active := s.nodes.active[key]; active {
		s.nodes.lowHit = min(s.nodes.lowHit, at)
		return resolution{}
	}
	at := len(s.nodes.active)
	s.nodes.active[key] = at
	saved := s.nodes.lowHit
	s.nodes.lowHit = at + 1
	r := compute()
	delete(s.nodes.active, key)
	if s.nodes.lowHit >= at {
		s.nodes.memo[key] = r
		s.nodes.lowHit = saved
	} else {
		s.nodes.lowHit = min(saved, s.nodes.lowHit)
	}
	return r
}
