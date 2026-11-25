package rejectinventory

// node resolves one shared producer node (a field, a field's elements or a
// parameter) once. A node re-entered while it is still resolving contributes
// nothing on that path; a result that depended on such an unfinished ancestor
// is not memoized, because the ancestor's other producers are still missing.
func (s *scan) node(key string, compute func() resolution) resolution {
	if r, ok := s.memo[key]; ok {
		return r
	}
	if at, active := s.active[key]; active {
		s.lowHit = min(s.lowHit, at)
		return resolution{}
	}
	at := len(s.active)
	s.active[key] = at
	saved := s.lowHit
	s.lowHit = at + 1
	r := compute()
	delete(s.active, key)
	if s.lowHit >= at {
		s.memo[key] = r
		s.lowHit = saved
	} else {
		s.lowHit = min(saved, s.lowHit)
	}
	return r
}
