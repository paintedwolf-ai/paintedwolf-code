package nfsserver

func (s *Server) BarrierState() (gatedViews, inFlightViews, handshakes int) {
	if s == nil || s.gate == nil {
		return 0, 0, 0
	}
	s.gate.mu.Lock()
	defer s.gate.mu.Unlock()
	return len(s.gate.gatedVH), len(s.gate.inFlightByVH), len(s.gate.handshakes)
}
