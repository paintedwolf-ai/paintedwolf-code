package surfacecatalog

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
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
	for _, want := range []string{"write", "command", "command_output", "git_commit", "task", "capture_page", "web_search", "secret_generate"} {
		if !contains(row.LoadableTools(), want) {
			t.Fatalf("implement_investigate loadable tools lack %s", want)
		}
	}
	for _, floor := range []string{"read", "grep", "request_tools"} {
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
  floor: [read]
  loadable: [scan_list]
no_folder_allowlist:
  floor: [read]
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
  floor: [read]
no_folder_allowlist:
  floor: [read, request_tools]
  loadable: [scan_list]
`})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "carries only a floor") {
		t.Fatalf("Load error = %v", err)
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
