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

// Hosts that only appear in ingested content are not contacts.
func TestContactedHostsAreOnlyDialedHosts(t *testing.T) {
	dialed, _ := DialedHostRecord("dialed.example")
	ev := AssembleLedger([]Record{
		{Handle: "web#1", URL: "https://a.example/x", SourceTool: "fetch_url"},
		{Handle: "web#2", URL: "https://B.example/y", SourceTool: "web_search"},
		{Handle: "file#1", Path: "a.go", SourceTool: "read"},
		dialed,
	})
	hosts := ContactedHostsFromLedger(ev)
	if len(hosts) != 1 {
		t.Fatalf("contacted %v, want only dialed.example", hosts)
	}
	if _, ok := hosts["dialed.example"]; !ok {
		t.Fatalf("missing dialed.example: %v", hosts)
	}
}
