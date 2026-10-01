package hostresources

// ConnectionModes describes catalog authority even when its service is currently
// unavailable. Discovery must not make a saved permission disappear from revoke.
func (s *Service) ConnectionModes(ids []string) []ConnectionMode {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	var modes []ConnectionMode
	for _, def := range s.defs {
		if !wanted[def.ID] {
			continue
		}
		realization := realizationForPlatform(def.Realizations, s.env.platform)
		if realization == nil {
			continue
		}
		for _, connection := range realization.Connections {
			modes = append(modes, connection.Mode)
		}
	}
	return modes
}
