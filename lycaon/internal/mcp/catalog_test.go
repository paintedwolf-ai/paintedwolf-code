package mcp_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func ptr[T any](v T) *T { return &v }

func TestMergeMCPCatalogFieldWinsAndNewIDDisabled(t *testing.T) {
	distro := &mcp.DistroMCPConfig{
		Providers: []mcp.MCPProviderEntry{
			{ID: "loopback", URL: "http://127.0.0.1:8765/mcp", Enabled: false},
		},
	}
	user := &mcp.UserMCPConfig{
		Providers: []mcp.MCPProviderOverlay{
			{ID: "loopback", Enabled: ptr(true)},
			{ID: "local-tool", Command: ptr("/opt/tool"), Enabled: nil},
		},
	}
	project := &mcp.UserMCPConfig{
		Providers: []mcp.MCPProviderOverlay{
			{ID: "loopback", URL: ptr("http://127.0.0.1:9999/mcp")},
		},
	}
	catalog, rejected, err := mcp.MergeMCPCatalog(distro, user, project)
	testutil.FailErr(t, "MergeMCPCatalog", err)
	if len(rejected) != 0 {
		t.Fatalf("unexpected rejected: %+v", rejected)
	}
	byID := map[string]mcp.MergedMCPProviderEntry{}
	for _, s := range catalog {
		byID[s.ID] = s
	}
	loopback := byID["loopback"]
	if !loopback.Enabled {
		t.Fatal("user enabled should win")
	}
	if loopback.URL != "http://127.0.0.1:9999/mcp" {
		t.Fatalf("project url should win, got %q", loopback.URL)
	}
	if loopback.ConnectionSource != mcp.CatalogLayerProject {
		t.Fatalf("connection_source=%s", loopback.ConnectionSource)
	}
	local := byID["local-tool"]
	if local.Enabled {
		t.Fatal("new id with omitted enabled must default disabled")
	}
	if local.ConnectionSource != mcp.CatalogLayerUser {
		t.Fatalf("local-tool source=%s", local.ConnectionSource)
	}
}

func TestMergeMCPCatalogTrustMatrix(t *testing.T) {
	distro := &mcp.DistroMCPConfig{
		Providers: []mcp.MCPProviderEntry{
			{ID: "loopback", URL: "http://127.0.0.1:8765/mcp", Enabled: true},
		},
	}

	cases := []struct {
		name   string
		user   *mcp.UserMCPConfig
		proj   *mcp.UserMCPConfig
		want   string
		wantID string
	}{
		{
			name:   "project_headers",
			proj:   &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{ID: "loopback", Headers: ptr(map[string]string{"Authorization": "Bearer x"})}}},
			want:   mcp.RejectProjectHeadersForbidden,
			wantID: "loopback",
		},
		{
			name:   "project_stdio_command",
			proj:   &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{ID: "evil", Command: ptr("/bin/evil")}}},
			want:   mcp.RejectProjectStdioForbidden,
			wantID: "evil",
		},
		{
			name:   "project_stdio_args",
			proj:   &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{ID: "loopback", Args: ptr([]string{"x"})}}},
			want:   mcp.RejectProjectStdioForbidden,
			wantID: "loopback",
		},
		{
			name:   "project_schemeless_loopback_ok",
			proj:   &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{ID: "loopback", URL: ptr("127.0.0.1:9999/mcp")}}},
			want:   "",
			wantID: "loopback",
		},
		{
			name:   "project_non_loopback_url",
			proj:   &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{ID: "loopback", URL: ptr("https://evil.example/mcp")}}},
			want:   mcp.RejectProjectRemoteForbidden,
			wantID: "loopback",
		},
		{
			name: "project_repoint_credentialed_entry",
			user: &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
				ID: "intel", URL: ptr("https://intel.example/mcp"), Token: ptr("secret"),
				Enabled: ptr(true),
			}}},
			proj:   &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{ID: "intel", URL: ptr("http://127.0.0.1:6666/mcp")}}},
			want:   mcp.RejectProjectHeadersForbidden,
			wantID: "intel",
		},
		{
			name: "remote_https_ok",
			user: &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
				ID: "remote", URL: ptr("https://intel.example/mcp"), Enabled: ptr(true),
			}}},
			want: "",
		},
		{
			name: "schemeless_loopback_ok",
			user: &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
				ID: "local", URL: ptr("127.0.0.1:8765/mcp"), Enabled: ptr(true),
			}}},
			want:   "",
			wantID: "local",
		},
		{
			name: "remote_requires_https",
			user: &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
				ID: "remote", URL: ptr("http://intel.example/mcp"), Enabled: ptr(true),
			}}},
			want:   mcp.RejectRemoteRequiresHTTPS,
			wantID: "remote",
		},
		{
			name: "invalid_url",
			user: &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
				ID: "remote", URL: ptr("ftp://intel.example/mcp"), Enabled: ptr(true),
			}}},
			want:   mcp.RejectInvalidURL,
			wantID: "remote",
		},
		{
			name: "invalid_both_url_and_command",
			user: &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
				ID: "bad", URL: ptr("http://127.0.0.1/mcp"), Command: ptr("/bin/x"),
			}}},
			want:   mcp.RejectInvalidEntry,
			wantID: "bad",
		},
		{
			name: "invalid_neither",
			user: &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
				ID: "bad", Enabled: ptr(true),
			}}},
			want:   mcp.RejectInvalidEntry,
			wantID: "bad",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog, rejected, err := mcp.MergeMCPCatalog(distro, tc.user, tc.proj)
			testutil.FailErr(t, "MergeMCPCatalog", err)
			if tc.want == "" {
				if len(rejected) != 0 {
					t.Fatalf("unexpected rejected: %+v", rejected)
				}
				wantID := tc.wantID
				if wantID == "" {
					wantID = "remote"
				}
				found := false
				for _, s := range catalog {
					if s.ID == wantID {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected %s in catalog: %+v", wantID, catalog)
				}
				return
			}
			found := false
			for _, r := range rejected {
				if r.Reason == tc.want && (tc.wantID == "" || r.ID == tc.wantID) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("want rejected %s id=%s; got %+v", tc.want, tc.wantID, rejected)
			}
			// A rejected project row for loopback must leave the distro loopback row in place.
			if tc.wantID == "loopback" && strings.HasPrefix(tc.want, "project_") {
				ok := false
				for _, s := range catalog {
					if s.ID == "loopback" && s.URL == "http://127.0.0.1:8765/mcp" {
						ok = true
					}
				}
				if !ok {
					t.Fatalf("loopback base should remain; catalog=%+v", catalog)
				}
			}
		})
	}
}

func TestMergeMCPCatalogRejectsExplicitEmptyToolLoading(t *testing.T) {
	user := &mcp.UserMCPConfig{Providers: []mcp.MCPProviderOverlay{{
		ID: "bad", URL: ptr("https://intel.example/mcp"), ToolLoading: ptr(api.McpToolLoading("")),
	}}}
	catalog, rejected, err := mcp.MergeMCPCatalog(nil, user, nil)
	testutil.FailErr(t, "MergeMCPCatalog", err)
	if len(catalog) != 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	if len(rejected) != 1 || rejected[0].ID != "bad" || rejected[0].Reason != mcp.RejectInvalidEntry {
		t.Fatalf("rejected = %+v", rejected)
	}
}

func TestMergeMCPCatalogRejectCodesClosedSet(t *testing.T) {
	codes := []string{
		mcp.RejectProjectStdioForbidden,
		mcp.RejectProjectHeadersForbidden,
		mcp.RejectProjectRemoteForbidden,
		mcp.RejectProjectEnableForbidden,
		mcp.RejectRemoteRequiresHTTPS,
		mcp.RejectInvalidURL,
		mcp.RejectInvalidEntry,
		mcp.RejectDuplicateID,
		mcp.RejectOverlayUnknownField,
	}
	seen := map[string]struct{}{}
	for _, c := range codes {
		if c == "" {
			t.Fatal("empty reject code")
		}
		if _, ok := seen[c]; ok {
			t.Fatalf("duplicate code %q", c)
		}
		seen[c] = struct{}{}
	}
}
