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

func TestVisitedHostsFromLedger(t *testing.T) {
	ev := AssembleLedger([]Record{
		{Handle: "web#1", URL: "https://a.example/x", SourceTool: "fetch_url"},
		{Handle: "web#2", URL: "https://B.example/y", SourceTool: "web_search"},
		{Handle: "file#1", Path: "a.go", SourceTool: "read"},
	})
	hosts := VisitedHostsFromLedger(ev)
	if _, ok := hosts["a.example"]; !ok {
		t.Fatalf("missing a.example: %v", hosts)
	}
	if _, ok := hosts["b.example"]; !ok {
		t.Fatalf("missing b.example: %v", hosts)
	}
}
