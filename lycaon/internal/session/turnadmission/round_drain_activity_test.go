package turnadmission

import "testing"

type activityNudges struct{ held map[string]int }

func (n *activityNudges) HasPendingLoopWakes(string) bool { return false }

func (n *activityNudges) BeginRoundDrain(sessionID string) func() {
	n.held[sessionID]++
	return func() { n.held[sessionID]-- }
}

func TestRoundDrainHoldsLoopActivity(t *testing.T) {
	s := &Service{}
	s.beginRoundDrain("chat")()
	nudges := &activityNudges{held: map[string]int{}}
	s.SetLoop(nudges, nil)
	end := s.beginRoundDrain("chat")
	if nudges.held["chat"] != 1 {
		t.Fatalf("held = %v during the drain, want chat once", nudges.held)
	}
	end()
	if nudges.held["chat"] != 0 {
		t.Fatalf("held = %v after the drain", nudges.held)
	}
}
