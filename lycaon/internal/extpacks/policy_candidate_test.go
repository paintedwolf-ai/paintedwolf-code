package extpacks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

// A pack written for Painted Wolf 1.0.2 keeps loading: its OAR 1.0 candidate
// rules are read in the OAR 1.0 shape, and a rule OAR 1.0 cannot express is
// refused on its own while the rest of the pack loads.
func TestInventoryReadsCandidateRulesAndRefusesOnlyTheUnconvertible(t *testing.T) {
	packRoot := t.TempDir()
	extpackstest.WriteMinimalPack(t, packRoot, "acme/rules", 1)
	policy := filepath.Join(packRoot, "policy")
	testutil.FailErr(t, "create policy", os.MkdirAll(policy, 0o755))
	rules := map[string]string{
		"KEEP.yaml":   "oar: '1.0'\nid: ACME_KEEP\nkind: policy\nanchor: tool.rejected\nwhen: 'true'\neffect: warn\n",
		"REDACT.yaml": "oar: '1.0'\nid: ACME_REDACT\nkind: policy\nanchor: tool.post_invoke\neffect: transform\n",
	}
	for name, body := range rules {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(policy, name), []byte(body), 0o600))
	}
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: "acme/rules", Root: extpacks.OnDisk(packRoot)}, extpacks.Manifest{ID: "acme/rules"})
	testutil.FailErr(t, "InventoryPack", err)
	var kept bool
	for _, u := range pc.Units {
		if u.ID == extpacks.PolicyUnitID("ACME_KEEP") {
			kept = strings.Contains(string(u.Content), `spec_version: "1.0"`) && strings.Contains(string(u.Content), "on: tool.rejected")
		}
		if strings.Contains(u.ID, "REDACT") {
			t.Fatalf("an unconvertible rule was published as %s", u.ID)
		}
	}
	if !kept {
		t.Fatalf("the candidate rule was not published in the OAR 1.0 shape: %+v", pc.Units)
	}
	var refused bool
	for _, d := range pc.Diagnostics {
		refused = refused || d.Code == extpacks.DiagPolicyCandidateRefused && strings.Contains(d.Message, "REDACT.yaml") && strings.Contains(d.Message, "transform")
	}
	if !refused {
		t.Fatalf("no refusal naming the file: %+v", pc.Diagnostics)
	}
}
