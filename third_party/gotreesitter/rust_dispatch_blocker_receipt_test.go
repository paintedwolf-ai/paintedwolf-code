package gotreesitter

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRustNormalizationBlockerReceiptKeepsArmLive preserves the Rust blocker
// until the receipt records a producer fix, full corpus coverage, and parity.
func TestRustNormalizationBlockerReceiptKeepsArmLive(t *testing.T) {
	registryData, err := os.ReadFile("testdata/result_compat_ownership_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Entries []struct {
			ID             string            `json:"id"`
			Functions      []string          `json:"functions"`
			RouteCoverage  map[string]string `json:"route_coverage"`
			Status         string            `json:"status"`
			RetirementCond string            `json:"retirement_condition"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(registryData, &registry); err != nil {
		t.Fatal(err)
	}
	rustIndex := -1
	for i, entry := range registry.Entries {
		if entry.ID != "dispatch.rust" {
			continue
		}
		rustIndex = i
		break
	}
	if rustIndex < 0 {
		t.Fatal("dispatch.rust is missing from the ownership registry")
	}
	rustEntry := registry.Entries[rustIndex]
	if rustEntry.Status != "live" {
		t.Fatalf("dispatch.rust status = %q, want live", rustEntry.Status)
	}
	if len(rustEntry.Functions) != 1 || rustEntry.Functions[0] != "normalizeRustCompatibility" {
		t.Fatalf("dispatch.rust functions = %v", rustEntry.Functions)
	}
	if rustEntry.RetirementCond == "" {
		t.Fatal("dispatch.rust has no retirement condition")
	}
	for _, route := range []string{"production", "compact", "forest", "incremental", "c_oracle"} {
		if rustEntry.RouteCoverage[route] == "" {
			t.Fatalf("dispatch.rust route coverage lacks %q", route)
		}
	}

	trackedData, err := os.ReadFile("testdata/dispatcher_census_tracked_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var tracked struct {
		Fixtures []struct {
			Language       string `json:"language"`
			Path           string `json:"path"`
			SHA256         string `json:"sha256"`
			ArmID          string `json:"arm_id"`
			Checked        uint64 `json:"checked"`
			Run            uint64 `json:"run"`
			NodesVisited   uint64 `json:"nodes_visited"`
			NodesRewritten uint64 `json:"nodes_rewritten"`
			ErrorRoot      bool   `json:"error_root"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(trackedData, &tracked); err != nil {
		t.Fatal(err)
	}
	rustIndex = -1
	for i, fixture := range tracked.Fixtures {
		if fixture.Language != "rust" {
			continue
		}
		rustIndex = i
		break
	}
	if rustIndex < 0 {
		t.Fatal("tracked Rust census fixture is missing")
	}
	rustFixture := tracked.Fixtures[rustIndex]
	if rustFixture.Path != "testdata/incremental_gate/rust_ast.rs" ||
		rustFixture.SHA256 != "43fc2344174da29bb3c032b260a009828e4636965c1ab8cfff62b651caf91b92" ||
		rustFixture.ArmID != "dispatch.rust" || rustFixture.Checked != 1 || rustFixture.Run != 1 ||
		rustFixture.NodesVisited != 17506 || rustFixture.NodesRewritten != 0 || rustFixture.ErrorRoot {
		t.Fatalf("unexpected tracked Rust census fixture: %+v", rustFixture)
	}

	a0Data, err := os.ReadFile("testdata/dispatcher_census_a0_manifest_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var a0 struct {
		Languages []string `json:"languages"`
	}
	if err := json.Unmarshal(a0Data, &a0); err != nil {
		t.Fatal(err)
	}
	for _, language := range a0.Languages {
		if language == "rust" {
			t.Fatal("A0 manifest unexpectedly claims Rust coverage")
		}
	}

	docData, err := os.ReadFile("docs/root-normalization-retirement.md")
	if err != nil {
		t.Fatal(err)
	}
	changelogData, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"`dispatch.rust` blocker receipt",
		"Status: NO-GO",
		"Keep `dispatch.rust` live",
		"88 entries",
		"31 dispatcher arms",
		"23 Rust witnesses",
		"two structural differences",
		"weird-exprs.rs",
		"closure_parameters/tuple_pattern/or_pattern",
		"full authenticated Rust corpus census",
		"TestNormalizeRustRecoveredFunctionItems",
	} {
		if !strings.Contains(string(docData), marker) {
			t.Fatalf("normalization retirement document lacks blocker marker %q", marker)
		}
	}
	if !strings.Contains(string(changelogData), "`dispatch.rust` blocker receipt") {
		t.Fatal("changelog lacks the dispatch.rust blocker receipt")
	}
}
