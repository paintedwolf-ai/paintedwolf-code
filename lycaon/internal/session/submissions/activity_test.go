package submissions

import "testing"

func TestHoldActivityMarksThePromptUntilReleased(t *testing.T) {
	s := &Service{}
	s.HoldActivity("chat")()
	held := map[string]int{}
	s.SetActivity(func(sessionID string) func() {
		held[sessionID]++
		return func() { held[sessionID]-- }
	})
	release := s.HoldActivity("chat")
	if held["chat"] != 1 {
		t.Fatalf("held = %v while the prompt runs, want chat once", held)
	}
	release()
	if held["chat"] != 0 {
		t.Fatalf("held = %v after release", held)
	}
}
