package hostprocess

import "testing"

func TestSignalVocabularyIsExact(t *testing.T) {
	seen := map[signalKind]bool{}
	for _, name := range []string{"TERM", "KILL", "INT", "HUP", "STOP", "CONT", "USR1", "USR2"} {
		kind, err := parseSignal(name)
		if err != nil || kind == 0 || seen[kind] {
			t.Fatalf("signal %q: kind=%d err=%v already accepted=%v", name, kind, err, seen[kind])
		}
		seen[kind] = true
	}
	if len(signalNames) != len(seen) {
		t.Fatalf("signal vocabulary has %d names, want %d", len(signalNames), len(seen))
	}
	for _, name := range []string{"", "term", "SIGTERM", "9", "0", "QUIT", " TERM", "TERM\x00"} {
		if kind, err := parseSignal(name); err == nil || kind != 0 {
			t.Fatalf("accepted unsupported signal %q as %d: %v", name, kind, err)
		}
	}
}
