package surfacecatalog

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestBundledCatalogLoadableSurfacesCarryRequestTools(t *testing.T) {
	catalog, err := Load()
	testutil.FailErr(t, "load coordinator surfaces", err)
	for _, id := range catalog.SurfaceIDs() {
		row, rowErr := catalog.Surface(id)
		testutil.FailErr(t, "load coordinator surface", rowErr)
		if len(row.Loadable) == 0 {
			continue
		}
		if !contains(row.Floor, "request_tools") {
			t.Fatalf("surface %q loads tools without request_tools", id)
		}
		if len(row.LoadableTools()) == 0 {
			t.Fatalf("surface %q names loadable entries that expand to nothing", id)
		}
	}
}

func TestBundledInvestigateLoadsEveryCapabilityItNames(t *testing.T) {
	catalog, err := Load()
	testutil.FailErr(t, "load coordinator surfaces", err)
	row, err := catalog.Surface("implement_investigate")
	testutil.FailErr(t, "implement_investigate", err)
	for _, want := range []string{"edit", "replace_lines", "code_rewrite", "jq_edit", "survey_repo", "git_commit", "task", "capture_page", "web_search", "secret_generate"} {
		if !contains(row.LoadableTools(), want) {
			t.Fatalf("implement_investigate loadable tools lack %s", want)
		}
	}
	for _, floor := range []string{"command", "request_tools"} {
		if !contains(row.Floor, floor) {
			t.Fatalf("implement_investigate floor lacks %s", floor)
		}
	}
}

func TestCatalogRejectsAmbiguousPlacement(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{config.CoordinatorSurface: `
test:
  mode_ref: test
  floor: [request_tools, scan_list]
  loadable: [scan_list, scan_query]
no_folder_allowlist:
  floor: [request_tools]
`})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "both on the floor and loadable") {
		t.Fatalf("Load error = %v", err)
	}
}

func TestCatalogRejectsUnreachableLoadableTools(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{config.CoordinatorSurface: `
test:
  mode_ref: test
  floor: [recall]
  loadable: [scan_list]
no_folder_allowlist:
  floor: [recall]
`})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "require request_tools on the floor") {
		t.Fatalf("Load error = %v", err)
	}
}

func TestCatalogRejectsLoadableRootlessFloor(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{config.CoordinatorSurface: `
test:
  mode_ref: test
  floor: [recall]
no_folder_allowlist:
  floor: [recall, request_tools]
  loadable: [scan_list]
`})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "carries only a floor") {
		t.Fatalf("Load error = %v", err)
	}
}

func TestBundledSurfacesOfferNoCommandOperation(t *testing.T) {
	catalog, err := Load()
	testutil.FailErr(t, "load coordinator surfaces", err)
	operations, err := toolcontract.CommandOperations()
	testutil.FailErr(t, "command operations", err)
	if len(operations) == 0 {
		t.Fatal("catalog declares no command operations")
	}
	for _, id := range catalog.SurfaceIDs() {
		row, rowErr := catalog.Surface(id)
		testutil.FailErr(t, "load coordinator surface", rowErr)
		for _, name := range append(row.FloorTools(), row.LoadableTools()...) {
			if toolcontract.IsCommandOperation(name) {
				t.Fatalf("surface %q offers command operation %q", id, name)
			}
		}
	}
}

func TestCatalogRejectsCommandOperations(t *testing.T) {
	for name, placement := range map[string]string{
		"floor":    "floor: [request_tools, read]",
		"loadable": "floor: [request_tools]\n  loadable: [git_log]",
	} {
		configtest.Only(t, map[config.Rel]string{config.CoordinatorSurface: "test:\n  mode_ref: test\n  " + placement + "\nno_folder_allowlist:\n  floor: [recall]\n"})
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "runs only through command") {
			t.Fatalf("%s: Load error = %v", name, err)
		}
	}
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
