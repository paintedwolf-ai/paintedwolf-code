package evidence

import "testing"

func TestDialedHostJoinsTheContactedSet(t *testing.T) {
	rec, ok := DialedHostRecord("Registry.Example.com")
	if !ok {
		t.Fatal("a plain host must produce a record")
	}
	ledger := Ledger{Handles: map[string]Record{rec.Handle: rec}}
	if ContactsFromLedger(ledger)["registry.example.com"] != ContactOwn {
		t.Fatal("a dialed host must be contacted")
	}
	if ContactsFromLedger(ledger)["other.example"] != ContactNone {
		t.Fatal("an undialed host must not be contacted")
	}
}

func TestContactRecordsDoNotMarkUntrusted(t *testing.T) {
	dialed, _ := DialedHostRecord("registry.example.com")
	retrieved, _ := RetrievedHostRecord("registry.example.com")
	if RecordMarksUntrustedContent(dialed) || RecordMarksUntrustedContent(retrieved) {
		t.Fatal("a contact record must not mark the session untrusted")
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
