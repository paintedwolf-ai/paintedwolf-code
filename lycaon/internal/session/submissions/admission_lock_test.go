package submissions

import "testing"

func TestAdmissionSerializesMatchingSessions(t *testing.T) {
	s := &Service{}
	unlock := s.lockAdmission("first")
	matching := s.admission.Acquire("first")
	if matching.TryLock() {
		matching.Unlock()
		t.Fatal("matching admission overlapped")
	}
	distinct := s.admission.Acquire("second")
	if !distinct.TryLock() {
		t.Fatal("unrelated admission was blocked")
	}
	distinct.Unlock()
	unlock()
	resumed := s.admission.Acquire("first")
	if !resumed.TryLock() {
		t.Fatal("completed admission retained its lock")
	}
	resumed.Unlock()
}
