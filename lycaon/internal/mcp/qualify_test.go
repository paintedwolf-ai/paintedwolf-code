package mcp_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
)

func TestQualifiedToolNameFoldsProviderIllegalRunes(t *testing.T) {
	cases := []struct {
		server, tool, want string
	}{
		{"svca", "do", "mcp_svca_do"},
		{"my-server", "list-repos", "mcp_my_server_list_repos"},
		{"coropa", "intel.search", "mcp_coropa_intel_search"},
		{"coropa", "host.exposure", "mcp_coropa_host_exposure"},
		{"GitHub", "Create.PR", "mcp_github_create_pr"},
		{"svc", "a/b", "mcp_svc_a_b"},
		{"svc", "...", "mcp_svc_tool"},
	}
	for _, tc := range cases {
		got := mcp.QualifiedToolName(tc.server, tc.tool)
		if got != tc.want {
			t.Fatalf("QualifiedToolName(%q, %q) = %q want %q", tc.server, tc.tool, got, tc.want)
		}
		if !providerSafeToolName(got) {
			t.Fatalf("QualifiedToolName(%q, %q) = %q is not provider-safe", tc.server, tc.tool, got)
		}
	}
}

func TestQualifiedToolNameFitsProviderLimit(t *testing.T) {
	long := strings.Repeat("a", 80)
	got := mcp.QualifiedToolName("server", long)
	if len(got) > 64 {
		t.Fatalf("len(%q) = %d want <= 64", got, len(got))
	}
	if !providerSafeToolName(got) {
		t.Fatalf("%q is not provider-safe", got)
	}
	again := mcp.QualifiedToolName("server", long)
	if got != again {
		t.Fatalf("long-name fold is not stable: %q vs %q", got, again)
	}
}

func TestQualifiedToolNameSharesPrefixWithCatalog(t *testing.T) {
	name := mcp.QualifiedToolName("my-server", "intel.search")
	prefix := mcp.ProviderPrefix("my-server")
	if !strings.HasPrefix(name, prefix) {
		t.Fatalf("qualified %q must start with catalog prefix %q", name, prefix)
	}
}

func TestQualifiedToolNameCollisionFoldsTogether(t *testing.T) {
	a := mcp.QualifiedToolName("svca", "intel.search")
	b := mcp.QualifiedToolName("svca", "intel_search")
	if a != b {
		t.Fatalf("fold collision expected, got %q vs %q", a, b)
	}
}

func providerSafeToolName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
