package projectpaths

// TestPath probes a previously resolved operand through the same held root used
// by content reads. It never routes an access query through mutation admission.
func (s *ReadSession) TestPath(resolved Resolved, operator string) (bool, error) {
	loc := resolved.EffectLocation()
	root, err := s.heldRoot(loc.Root)
	if err != nil {
		return false, err
	}
	return root.TestPath(loc.Rel, operator)
}
