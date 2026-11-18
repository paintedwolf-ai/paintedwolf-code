package oar

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConformanceRunnerSpecPosture(t *testing.T) {
	root := testutil.CheckoutRoot(t)
	out := t.TempDir()
	// Load both packs through the stock catalog.
	codes := []string{
		"SPEC_POSTURE_STATE_FORBIDDEN",
		"SPEC_POSTURE_DELEGATION_FORBIDDEN",
		"DISALLOWED_AGENT",
	}
	seedStockConformanceFixtures(t, out, codes)
	cr, err := NewHostConformanceRunner(filepath.Join(root, "schemas"), InstalledEngineDeclaration())
	testutil.FailErr(t, "runner", err)
	fxs, err := LoadFixturesDir(out)
	testutil.FailErr(t, "load fixtures", err)
	if len(fxs) < 3 {
		t.Fatalf("expected fixtures, got %d", len(fxs))
	}
	for _, fx := range fxs {
		if err := cr.RunFixture(context.Background(), fx); err != nil {
			t.Errorf("%s: %v", fx.ID, err)
		}
	}
}

func seedStockConformanceFixtures(t *testing.T, outDir string, codes []string) {
	t.Helper()
	testutil.FailErr(t, "install anchor catalog", anchorcatalog.InstallBundled())
	loader, err := NewLoader("")
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load stock rules", err)
	for _, code := range codes {
		r, ok := rules.Get(code)
		if !ok || r.When == "" || r.raw == nil {
			t.Fatalf("stock rule %q cannot seed a fixture", code)
		}
		doc, err := json.Marshal(r.raw)
		testutil.FailErr(t, "encode rule", err)
		facts := map[string]any{"tool": "", oarcopy.HostFactNamespace + ".session_posture": "spec"}
		if tools := r.Tools(); len(tools) > 0 {
			facts["tool"] = tools[0]
		}
		if r.Anchor == AnchorToolRejected {
			facts[oarcopy.HostFactNamespace+".rejection_code"] = code
		}
		decision, err := json.Marshal(string(r.Effect))
		testutil.FailErr(t, "encode expected decision", err)
		ruleCode, err := json.Marshal(r.ID)
		testutil.FailErr(t, "encode expected code", err)
		fx := ConformanceFixture{
			ID: code, Rule: doc,
			Input:    FixtureInput{Anchor: r.Anchor, Facts: facts},
			Expected: map[string]json.RawMessage{"decision": decision, "code": ruleCode},
		}
		raw, err := json.MarshalIndent(fx, "", "  ")
		testutil.FailErr(t, "encode conformance fixture", err)
		testutil.FailErr(t, "write conformance fixture", os.WriteFile(filepath.Join(outDir, code+".json"), append(raw, '\n'), 0o600))
	}
}
