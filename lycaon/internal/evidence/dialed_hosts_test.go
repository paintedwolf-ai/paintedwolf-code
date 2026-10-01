package evidence

import "testing"

func TestDialedHostJoinsTheVisitedSet(t *testing.T) {
	rec, ok := DialedHostRecord("Registry.Example.com")
	if !ok {
		t.Fatal("a plain host must produce a record")
	}
	ledger := Ledger{Handles: map[string]Record{rec.Handle: rec}}
	if !HostVisited(VisitedHostsFromLedger(ledger), "registry.example.com") {
		t.Fatal("a dialed host must be visited")
	}
	if HostVisited(VisitedHostsFromLedger(ledger), "other.example") {
		t.Fatal("an undialed host must not be visited")
	}
}

func TestDialedHostDoesNotMarkUntrusted(t *testing.T) {
	rec, _ := DialedHostRecord("registry.example.com")
	if RecordMarksUntrustedContent(rec) {
		t.Fatal("a dial record must not mark the session untrusted")
	}
}

func TestDialedHostRecordIsStablePerHost(t *testing.T) {
	a, _ := DialedHostRecord("example.com")
	b, _ := DialedHostRecord("EXAMPLE.com")
	if a.Handle != b.Handle {
		t.Fatalf("handles differ: %q vs %q", a.Handle, b.Handle)
	}
}

func TestDialedHostRejectsNonHosts(t *testing.T) {
	for _, raw := range []string{"", "   ", "example.com/path", "a b"} {
		if _, ok := DialedHostRecord(raw); ok {
			t.Errorf("DialedHostRecord(%q) should not produce a record", raw)
		}
	}
}
