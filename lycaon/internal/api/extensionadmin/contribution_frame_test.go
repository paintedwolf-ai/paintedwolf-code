package extensionadmin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func contributionTestFrame(t *testing.T, catalogRevision, mcpRevision string, trackerReady bool) *contribframe.Frame {
	t.Helper()
	set, err := contribution.Compile(contribution.CompileInput{
		Units: []contribution.Input{
			{
				UnitID: "contributions/commands/go-home", Kind: contribution.KindCommand,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:go-home\ntitle: Go home\naction: {kind: navigate, destination: home}\n"),
			},
			{
				UnitID: "contributions/commands/search", Kind: contribution.KindCommand,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:search\ntitle: Search\naction:\n  kind: mcp_tool\n  requirement: acme/reviewer:tracker\n  tool: search_issues\n"),
			},
			{
				UnitID: "contributions/mcp-requirements/tracker", Kind: contribution.KindMCPRequirement,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:tracker\nprovider_id: tracker\nrequired_tools: [search_issues]\npurpose: Tracker\n"),
			},
		},
	})
	testutil.FailErr(t, "compile", err)
	status := wire.McpStatusError
	if trackerReady {
		status = wire.McpStatusReady
	}
	frame, err := contribframe.Build(
		&catalogview.View{
			Catalog:       &extpacks.EffectiveCatalog{Revision: catalogRevision, Loaded: map[string]extpacks.UnitEffective{}},
			Contributions: set,
		},
		&mcp.ResourceGeneration{Revision: mcpRevision, Providers: []mcp.GenerationProvider{{
			ID: "tracker", Enabled: true, Status: status,
			Tools: []mcp.GenerationTool{{Name: "search_issues"}},
		}}},
	)
	testutil.FailErr(t, "build frame", err)
	return frame
}

func TestRememberContributionFrameBoundsRetention(t *testing.T) {
	srv := &Handler{}
	var revisions []string
	for i := 0; i < contributionFrameLRUCap+3; i++ {
		frame := contributionTestFrame(t, fmt.Sprintf("cat-%d", i), "mcp-1", true)
		revisions = append(revisions, frame.Revision)
		srv.rememberContributionFrame(frame)
	}
	if _, ok := srv.contributionFrameByRevision(revisions[0]); ok {
		t.Fatal("oldest frame must be evicted at the cap")
	}
	if _, ok := srv.contributionFrameByRevision(revisions[len(revisions)-1]); !ok {
		t.Fatal("newest frame must be retained")
	}
	if len(srv.contribFrames) != contributionFrameLRUCap {
		t.Fatalf("retained = %d want %d", len(srv.contribFrames), contributionFrameLRUCap)
	}
}

func TestContributionFrameRetentionTouchesReads(t *testing.T) {
	srv := &Handler{}
	frames := make([]*contribframe.Frame, 0, contributionFrameLRUCap+1)
	for i := 0; i < contributionFrameLRUCap; i++ {
		frame := contributionTestFrame(t, fmt.Sprintf("cat-%d", i), "mcp-1", true)
		frames = append(frames, frame)
		srv.rememberContributionFrame(frame)
	}
	if _, ok := srv.contributionFrameByRevision(frames[0].Revision); !ok {
		t.Fatal("oldest frame must be readable before eviction")
	}
	newest := contributionTestFrame(t, "cat-new", "mcp-1", true)
	srv.rememberContributionFrame(newest)

	if _, ok := srv.contributionFrameByRevision(frames[0].Revision); !ok {
		t.Fatal("recently read frame must be retained")
	}
	if _, ok := srv.contributionFrameByRevision(frames[1].Revision); ok {
		t.Fatal("least recently used frame must be evicted")
	}
}

func TestSubgraphCompareIsolatesUnaffectedCommands(t *testing.T) {
	srv := &Handler{}
	prior := contributionTestFrame(t, "cat-1", "mcp-ready", true)
	churned := contributionTestFrame(t, "cat-1", "mcp-broken", false)
	srv.rememberContributionFrame(prior)

	navigate, err := contribution.ParseID("acme/reviewer:go-home")
	testutil.FailErr(t, "parse", err)
	mcpBacked, err := contribution.ParseID("acme/reviewer:search")
	testutil.FailErr(t, "parse", err)

	if !srv.subgraphUnchanged(prior.Revision, churned, navigate) {
		t.Fatal("readiness churn must not reject an unaffected navigate command")
	}
	if srv.subgraphUnchanged(prior.Revision, churned, mcpBacked) {
		t.Fatal("a requirement readiness flip must reject the MCP-backed command")
	}
	if srv.subgraphUnchanged("unknown-revision", churned, navigate) {
		t.Fatal("an unretained caller frame must not proceed")
	}
}

// Package init panics on a gap. Asserting it here too names the fact, rather
// than surfacing as a panic in whichever test binary loads this package first.
func TestHostFactBindersCoverTheVocabularyExactly(t *testing.T) {
	bound := make([]string, 0, len(hostFactBinders))
	for name := range hostFactBinders {
		bound = append(bound, name)
	}
	if err := contribution.RequireHostFactCoverage(bound); err != nil {
		t.Fatalf("host fact binding: %v", err)
	}
}

func TestHostFactCoverageNamesWhatIsMissing(t *testing.T) {
	err := contribution.RequireHostFactCoverage(nil)
	if err == nil {
		t.Fatal("an empty binder set must not satisfy the host plane")
	}
	if !strings.Contains(err.Error(), "project_open") {
		t.Fatalf("error must name the unbound facts, got %v", err)
	}
	if err := contribution.RequireHostFactCoverage([]string{"active_view"}); err == nil {
		t.Fatal("a shell fact must not pass as a host binder")
	}
}

func TestHostFactLookupBindsTypedState(t *testing.T) {
	frame := contributionTestFrame(t, "cat-1", "mcp-ready", true)
	sess := &wire.Session{ID: "s1", Status: wire.SessionStatusIdle}
	lookup := hostFactLookup(frame, sess, wire.CommandInvokeContext{
		Path: "main.go", StartLine: 3, EndLine: 9, FindingID: "f-1",
	})
	cases := []struct {
		fact, operand string
		want          bool
	}{
		{"project_open", "", true},
		{"session_exists", "", true},
		{"session_idle", "", true},
		{"activity_live", "", false},
		{"editor_active", "", true},
		{"editor_has_selection", "", true},
		{"editor_has_symbol", "", false},
		{"editor_has_finding", "", true},
		{"editor_language", "go", true},
		{"editor_language", "python", false},
		{"mcp_requirement_ready", "acme/reviewer:tracker", true},
		{"mcp_requirement_ready", "acme/reviewer:absent", false},
	}
	for _, tc := range cases {
		if got := lookup(tc.fact, tc.operand); got != tc.want {
			t.Fatalf("%s(%q) = %v want %v", tc.fact, tc.operand, got, tc.want)
		}
	}
	noSession := hostFactLookup(frame, nil, wire.CommandInvokeContext{})
	if noSession("session_exists", "") || noSession("editor_active", "") {
		t.Fatal("absent session and target must read false")
	}
}
