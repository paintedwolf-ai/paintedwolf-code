package contract

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func TestReconciledStockConfigInventoryUnion(t *testing.T) {
	_, schemaDir, moduleRoot, packsRoot := oarAuditEnv(t)
	vocab, _ := promptNameCatalog(t)
	configRoot := filepath.Join(moduleRoot, "config")

	unionRep, err := promptrender.ReconcileConfigInventory(configRoot)
	if err != nil {
		t.Fatalf("ReconcileConfigInventory: %v", err)
	}
	if len(unionRep.Violations) > 0 {
		t.Fatalf("stock config inventory union found %d violation(s):\n%s",
			len(unionRep.Violations), strings.Join(unionRep.Violations, "\n"))
	}
	if unionRep.TotalFiles == 0 || unionRep.TotalFiles != unionRep.AuditedTotal+unionRep.ExcludedTotal {
		t.Fatalf("inventory partition mismatch: total=%d audited=%d excluded=%d",
			unionRep.TotalFiles, unionRep.AuditedTotal, unionRep.ExcludedTotal)
	}

	oarSum := auditStockOARRoot(t, vocab, packsRoot, schemaDir, true)
	if len(oarSum.Violations) > 0 {
		t.Fatalf("CT04 OAR audit violations:\n%s", formatOARViolations(oarSum.Violations))
	}
	if got := unionRep.AuditedByFamily["ct04/policy"]; got != oarSum.PolicyFiles {
		t.Fatalf("ct04/policy count = %d, want CT04 PolicyFiles = %d", got, oarSum.PolicyFiles)
	}
	if got := unionRep.AuditedByFamily["ct04/host/next-steps"]; got != oarSum.NextStepsFiles {
		t.Fatalf("ct04/host/next-steps count = %d, want CT04 NextStepsFiles = %d", got, oarSum.NextStepsFiles)
	}
	if got := unionRep.AuditedByFamily["ct04/host/anchors"]; got != oarSum.AnchorFiles {
		t.Fatalf("ct04/host/anchors count = %d, want CT04 AnchorFiles = %d", got, oarSum.AnchorFiles)
	}
	if unionRep.AuditedByFamily["ct01/host/bindings"] == 0 {
		t.Fatal("expected non-empty ct01/host/bindings inventory")
	}

	var famKeys, catKeys []string
	for k := range unionRep.AuditedByFamily {
		famKeys = append(famKeys, k)
	}
	for k := range unionRep.ExcludedByCategory {
		catKeys = append(catKeys, k)
	}
	slices.Sort(famKeys)
	slices.Sort(catKeys)
	t.Logf("reconciled lycaon/config inventory: total=%d audited=%d excluded=%d",
		unionRep.TotalFiles, unionRep.AuditedTotal, unionRep.ExcludedTotal)
	for _, k := range famKeys {
		t.Logf("  audited  %-36s %5d", k, unionRep.AuditedByFamily[k])
	}
	for _, k := range catKeys {
		t.Logf("  excluded %-36s %5d", k, unionRep.ExcludedByCategory[k])
	}
}

func TestReconciledStockConfigInventoryRejectsUnclassifiedSources(t *testing.T) {
	cases := []struct {
		name      string
		rel       string
		body      string
		expectSub string
	}{
		{
			name:      "unclassified markdown in stock pack",
			rel:       "packs/painted-wolf/platform/unreviewed/rogue-guide.md",
			body:      "Call `verify` now.\n",
			expectSub: "config/packs/painted-wolf/platform/unreviewed/rogue-guide.md",
		},
		{
			name:      "unclassified markdown outside packs",
			rel:       "runtime/rogue-notes.md",
			body:      "Internal runtime note.\n",
			expectSub: "config/runtime/rogue-notes.md",
		},
		{
			name:      "unclassified yaml under pack host root",
			rel:       "packs/painted-wolf/platform/host/unconsumed-rogue.yaml",
			body:      "enabled: true\n",
			expectSub: "config/packs/painted-wolf/platform/host/unconsumed-rogue.yaml",
		},
		{
			name:      "unclassified yaml under unreviewed pack directory",
			rel:       "packs/painted-wolf/platform/unreviewed/rogue-rule.yaml",
			body:      "id: ROGUE\n",
			expectSub: "config/packs/painted-wolf/platform/unreviewed/rogue-rule.yaml",
		},
		{
			name:      "host binding targeting missing guidance render",
			rel:       "packs/painted-wolf/platform/host/bindings/rogue-binding.yaml",
			body:      "on: turn.closeout\nselector:\n  surface: coordinator\neffect: inform\nrender: missing-template\n",
			expectSub: "missing-template",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			if strings.Contains(tc.rel, "/host/bindings/") {
				writeSeededFile(t, tmp, "packs/painted-wolf/platform/guidance/existing.md", "Grounded guidance.\n")
			}
			writeSeededFile(t, tmp, tc.rel, tc.body)
			rep, err := promptrender.ReconcileConfigInventory(tmp)
			if err != nil {
				t.Fatalf("ReconcileConfigInventory: %v", err)
			}
			joined := strings.Join(rep.Violations, "\n")
			if !strings.Contains(joined, tc.expectSub) {
				t.Fatalf("expected violation naming %q, got:\n%s", tc.expectSub, joined)
			}
		})
	}
}
