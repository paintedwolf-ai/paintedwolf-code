package extpacks_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateProjectSuggestionsAreInformational(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	projectDir := t.TempDir()
	path := extpacks.ProjectDesiredPath(projectDir)
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(filepath.Dir(path), 0o700))
	data, err := extpacks.EncodeSuggestion(extpacks.SuggestionManifest{
		Format: extpacks.DesiredFormat,
		Suggest: []extpacks.SuggestedPack{{
			ID:      "acme/triage",
			Source:  "https://example.com/acme/triage.git",
			Version: "^1.0.0",
		}},
	})
	testutil.FailErr(t, "encode suggestion", err)
	testutil.FailErr(t, "write suggestion", os.WriteFile(path, data, 0o600))

	eff, err := extpacks.ResolveCatalog(t.Context(), []string{projectDir}, nil)
	testutil.FailErr(t, "ResolveCatalog", err)
	rep, err := extpacks.Validate(t.Context(), projectDir, eff)
	testutil.FailErr(t, "Validate", err)
	if !rep.OK {
		t.Fatalf("suggestion must keep validate green:\n%s", extpacks.FormatValidateText(rep))
	}
	found := false
	for _, d := range rep.Diagnostics {
		if d.Code == extpacks.DiagProjectPackSuggested && d.PackID == "acme/triage" {
			found = true
			if d.Severity != extpacks.SeverityInfo {
				t.Fatalf("suggestion diagnostic severity=%s want info", d.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("missing project_pack_suggested: %#v", rep.Diagnostics)
	}
}

func TestLoadMergedDesiredUnionsProjectDisabledState(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	primary := t.TempDir()
	active := t.TempDir()
	for _, row := range []struct {
		dir  string
		body string
	}{
		{primary, "format: 1\ndisabled: [guidance/primary]\n"},
		{active, "format: 1\ndisabled: [guidance/active]\n"},
	} {
		path := extpacks.ProjectDesiredPath(row.dir)
		testutil.FailErr(t, "mkdir overlay", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write suggestion", os.WriteFile(path, []byte(row.body), 0o600))
	}

	desired, _, err := extpacks.LoadMergedDesired([]string{primary, active})
	testutil.FailErr(t, "load merged desired", err)
	if len(desired.Disabled) != 2 || desired.Disabled[0] != "guidance/active" || desired.Disabled[1] != "guidance/primary" {
		t.Fatalf("disabled = %v want active and primary suggestions", desired.Disabled)
	}
}

func TestSuggestionRevisionIgnoresAutomaticDisabledSelectors(t *testing.T) {
	proposal := extpacks.SuggestionManifest{
		Format: extpacks.DesiredFormat,
		Suggest: []extpacks.SuggestedPack{{
			ID: "acme/triage", Source: "https://example.com/acme/triage.git", Version: "^1.0.0",
		}},
	}
	baseline := extpacks.SuggestionRevision(proposal)
	proposal.Disabled = []string{"workflows/bugbash"}
	if got := extpacks.SuggestionRevision(proposal); got != baseline {
		t.Fatalf("automatic disabled selector moved install review revision: before=%q after=%q", baseline, got)
	}
	proposal.Suggest[0].Version = "^2.0.0"
	if got := extpacks.SuggestionRevision(proposal); got == baseline {
		t.Fatal("proposal version change did not move install review revision")
	}
}

func TestSuggestionRevisionIgnoresProposalOrder(t *testing.T) {
	a := extpacks.SuggestionManifest{Format: extpacks.DesiredFormat, Suggest: []extpacks.SuggestedPack{
		{ID: "acme/b", Source: "https://example.com/b.git"},
		{ID: "acme/a", Source: "https://example.com/a.git"},
	}}
	b := extpacks.SuggestionManifest{Format: extpacks.DesiredFormat, Suggest: []extpacks.SuggestedPack{a.Suggest[1], a.Suggest[0]}}
	if extpacks.SuggestionRevision(a) != extpacks.SuggestionRevision(b) {
		t.Fatal("proposal row reordering moved the semantic install-review revision")
	}
}
