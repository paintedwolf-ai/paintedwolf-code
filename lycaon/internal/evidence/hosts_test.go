package evidence

import "testing"

func TestHostKey(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://Example.COM/a", "example.com"},
		{"http://docs.example.com:8080/x", "docs.example.com"},
		{"not a url", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := HostKey(tc.raw); got != tc.want {
			t.Fatalf("HostKey(%q)=%q want %q", tc.raw, got, tc.want)
		}
	}
}

// Hosts that only appear in ingested content are not contacts; the chat's
// own dial outranks an audited dial, which outranks a retrieval of the same
// host.
func TestContactsAreOnlyDialedHosts(t *testing.T) {
	dialed, _ := DialedHostRecord("dialed.example")
	retrieved, _ := RetrievedHostRecord("retrieved.example")
	both, _ := RetrievedHostRecord("both.example")
	bothOwn, _ := DialedHostRecord("both.example")
	audited, _ := AuditedHostRecord("audited.example")
	auditedRetrieved, _ := RetrievedHostRecord("audited.example")
	auditedOwn, _ := AuditedHostRecord("both.example")
	ev := AssembleLedger([]Record{
		{Handle: "web#1", URL: "https://a.example/x", SourceTool: "fetch_url"},
		{Handle: "web#2", URL: "https://B.example/y", SourceTool: "web_search"},
		{Handle: "file#1", Path: "a.go", SourceTool: "read"},
		dialed, retrieved, both, bothOwn, audited, auditedRetrieved, auditedOwn,
	})
	want := map[string]Contact{"dialed.example": ContactOwn, "retrieved.example": ContactRetrieval, "both.example": ContactOwn, "audited.example": ContactAudited}
	got := ContactsFromLedger(ev)
	if len(got) != len(want) {
		t.Fatalf("contacts %v, want %v", got, want)
	}
	for host, contact := range want {
		if got[host] != contact {
			t.Fatalf("%s: contact %d, want %d", host, got[host], contact)
		}
	}
}
