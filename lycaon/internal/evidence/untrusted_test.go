package evidence

import "testing"

func TestRecordMarksUntrustedContent(t *testing.T) {
	cases := []struct {
		name string
		rec  Record
		want bool
	}{
		{"web_search", Record{SourceTool: "web_search"}, true},
		{"fetch_url", Record{SourceTool: "fetch_url"}, true},
		{"mcp", Record{SourceTool: "mcp_docs_get"}, true},
		{"inherit marker", Record{Handle: InheritUntrustedHandle}, true},
		{"worker url marker", Record{Handle: WorkerURLHandlePrefix + "deadbeef"}, true},
		{"namespaced inherit marker", Record{Handle: "root:child:" + InheritUntrustedHandle}, true},
		{"namespaced worker url marker", Record{Handle: "root:child:" + WorkerURLHandlePrefix + "deadbeef"}, true},
		{"read", Record{SourceTool: "read"}, false},
		{"empty", Record{}, false},
		{"trust ignored", Record{SourceTool: "web_search", Fidelity: FidelityStructured}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecordMarksUntrustedContent(tc.rec); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
