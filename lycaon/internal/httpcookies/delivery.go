package httpcookies

// SetDelivery configures this opened jar's invocation only. The issuer is
// written solely by SetCookies; selecting or withholding never changes it.
func (s *Store) SetDelivery(nativeIssuer string, allow func(Cookie) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeIssuer, s.allowDelivery = nativeIssuer, allow
}

func (s *Store) deliveryAllowed(cookie Cookie) bool {
	s.mu.Lock()
	allow := s.allowDelivery
	s.mu.Unlock()
	return allow == nil || allow(cookie)
}
