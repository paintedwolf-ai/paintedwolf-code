package bundleddriver

import (
	"os"
	"path/filepath"
	"testing"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOpengrepEvidenceMapsEveryLocation(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "page.vue")
	testutil.FailErr(t, "write original", os.WriteFile(original, []byte("<script>eval(location.hash)</script>"), 0o600))
	projection := filepath.Join(t.TempDir(), "page.vue.script-0.ts")
	location := api.SecurityFindingLocation{URI: projection, StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 5}
	result := &scanoutput.Result{Warnings: []api.ScanWarning{{Kind: api.ScanWarningFilePartialParse, File: projection, StartLine: 1, StartColumn: 6}}, Findings: []api.SecurityFinding{{Locations: []api.SecurityFindingLocation{location}, Dataflow: &api.SecurityFindingDataflow{
		Source:        &api.SecurityFindingCallTrace{Location: location, Callee: &api.SecurityFindingCallTrace{Location: location}, Intermediates: []api.SecurityFindingLocation{location}},
		Intermediates: []api.SecurityFindingLocation{location}, Sink: &api.SecurityFindingCallTrace{Location: location},
	}}}}
	projected, _, err := sourceview.Scripts(t.Context(), []byte("<script>eval(location.hash)</script>"), true)
	testutil.FailErr(t, "project source", err)
	testutil.FailErr(t, "remap every location", scanoutput.RemapOpengrepSources(result, map[string]sourceview.Origin{projection: {Path: original, Map: projected[0].Map}}, root))
	count := 0
	scanfindings.VisitFindingLocations(&result.Findings[0], func(loc *api.SecurityFindingLocation) {
		count++
		if loc.URI != original || loc.StartColumn != 9 || loc.EndColumn != 13 {
			t.Errorf("projection escaped: %s", loc.URI)
		}
	})
	if result.Warnings[0].File != original || result.Warnings[0].StartColumn != 14 {
		t.Fatalf("warning origin=%+v", result.Warnings[0])
	}
	if count != 6 {
		t.Fatalf("visited %d locations", count)
	}
	testutil.FailErr(t, "validate remapped evidence", scanoutput.ValidateOpengrepLocations(result, root))
	outside := filepath.Join(t.TempDir(), "outside.py")
	testutil.FailErr(t, "write outside file", os.WriteFile(outside, []byte("pass"), 0o600))
	result.Findings[0].Dataflow.Source.Callee.Location.URI = outside
	if err := scanoutput.ValidateOpengrepLocations(result, root); err == nil {
		t.Fatal("accepted trace outside snapshot")
	}
	link := filepath.Join(root, "link.py")
	testutil.FailErr(t, "link outside file", os.Symlink(outside, link))
	result.Findings[0].Dataflow.Source.Callee.Location.URI = link
	if err := scanoutput.ValidateOpengrepLocations(result, root); err == nil {
		t.Fatal("accepted trace symlink outside snapshot")
	}
}

func TestOpengrepIncompleteSourceContextRetainsFindings(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write malformed source", os.WriteFile(filepath.Join(root, "broken.py"), []byte("eval(user_input)\nif (\n"), 0o600))
	result := &scanoutput.Result{Findings: []api.SecurityFinding{
		{RuleID: "opengrep:generic-flow", Locations: []api.SecurityFindingLocation{{URI: "broken.py", StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 17}}},
		{RuleID: "opengrep:generic-flow", Locations: []api.SecurityFindingLocation{{URI: "broken.py", StartLine: 1, StartColumn: 6, EndLine: 1, EndColumn: 16}}},
	}}
	bundle := []byte("rules:\n- id: generic-flow\n  languages: [generic]\n  pattern: eval(...)\n  message: flow\n  severity: ERROR\n")
	testutil.FailErr(t, "filter incomplete source", filterNonCode(t.Context(), result, root, bundle))
	if result.FindingsCount != 2 || len(result.Warnings) != 1 || result.Warnings[0].Kind != api.ScanWarningFilePartialParse || result.Warnings[0].File != "broken.py" {
		t.Fatalf("findings=%d warnings=%+v", result.FindingsCount, result.Warnings)
	}
}
