package extpacks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// mapScannerChecker reports a scanner enabled when the map marks it met.
type mapScannerChecker map[string]bool

func (m mapScannerChecker) ScannerEnabled(_ context.Context, _, scannerID string) bool {
	return m[scannerID]
}

func TestResolveAllStockEnabledFullCatalog(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	if err := eff.BootError(); err != nil {
		testutil.FailErr(t, "eff.BootError failed", err)
	}
	if !eff.HasLoaded(WorkflowUnitID("plan")) {
		t.Fatal("expected workflows/plan loaded")
	}
	if !eff.HasLoaded(PolicyUnitID("WRITE_SCOPE_DENIED")) {
		t.Fatal("expected security OAR WRITE_SCOPE_DENIED")
	}
	if !eff.HasLoaded(PolicyUnitID("SCAN_LIST_EMPTY")) {
		t.Fatal("expected scanners SCAN_LIST_EMPTY")
	}
	if !eff.PackContributed("painted-wolf/platform") {
		t.Fatal("platform must contribute")
	}
	n := len(eff.Loaded)
	if n < 200 {
		t.Fatalf("expected large full catalog, got %d loaded units", n)
	}
}

func TestResolvePlanDisabledOmitsPlanVertical(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), desiredWithDisabledPack("painted-wolf/plan"), nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	if eff.HasLoaded(WorkflowUnitID("plan")) {
		t.Fatal("workflows/plan must be omitted when plan pack disabled")
	}
	if eff.PackContributed("painted-wolf/plan") {
		t.Fatal("plan pack must not contribute")
	}
	if !eff.HasLoaded(WorkflowUnitID("implement")) {
		t.Fatal("implement workflow should still load")
	}
}

func TestResolveSecurityDisabledOmitsJailOAR(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), desiredWithDisabledPack("painted-wolf/security"), nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	if eff.HasLoaded(PolicyUnitID("WRITE_SCOPE_DENIED")) {
		t.Fatal("WRITE_SCOPE_DENIED must be omitted when security disabled")
	}
	if eff.HasLoaded(PolicyUnitID("SANDBOX_TRY_WRITE_ROOT")) {
		t.Fatal("sandbox OAR must be omitted when security disabled")
	}
	if err := eff.BootError(); err != nil {
		t.Fatalf("disabling security must still boot: %v", err)
	}
}

func TestResolveScannersDisabledOmitsScanUnits(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), desiredWithDisabledPack("painted-wolf/scan-guidance"), nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	if eff.HasLoaded(PolicyUnitID("SCAN_LIST_EMPTY")) {
		t.Fatal("SCAN_LIST_EMPTY must be omitted")
	}
	for id := range eff.Loaded {
		if strings.HasPrefix(id, "policy/SCAN_") {
			t.Fatalf("unexpected scan policy loaded: %s", id)
		}
	}
}

func TestResolveUnitDisabledInspectable(t *testing.T) {
	unit := GuidanceUnitID("coordinator-gate-blocked")
	desired := EmptyDesired()
	desired.Disabled = []string{unit}
	eff, err := resolveWithDesired(t.Context(), desired, nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	if eff.HasLoaded(unit) {
		t.Fatal("disabled unit must not be loaded")
	}
	u, ok := eff.Units[unit]
	if !ok || u.Status != UnitStatusDisabled {
		t.Fatalf("expected disabled status, got %+v ok=%v", u, ok)
	}
	if len(eff.InspectContributions(unit)) == 0 {
		t.Fatal("disabled unit must remain inspectable via contributions")
	}
}

func TestResolvePlatformDisabledBootFailsLoud(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), desiredWithDisabledPack("painted-wolf/platform"), nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	if eff.PackContributed("painted-wolf/platform") {
		t.Fatal("platform must not contribute")
	}
	if eff.PackContributed("painted-wolf/plan") {
		t.Fatal("plan must not contribute without platform")
	}
	if err := eff.BootError(); err == nil {
		t.Fatal("BootError must fail when platform disabled")
	}
}

func TestResolvePermutePackOrderIdentical(t *testing.T) {
	content, err := DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent failed", err)
	rev := append([]PackContent(nil), content...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	a := Resolve(t.Context(), ResolveInput{Packs: content, Desired: EmptyDesired()})
	b := Resolve(t.Context(), ResolveInput{Packs: rev, Desired: EmptyDesired()})
	if a.Revision != b.Revision {
		t.Fatalf("fingerprint mismatch under pack order permute: %s vs %s", a.Revision, b.Revision)
	}
	if len(a.Loaded) != len(b.Loaded) {
		t.Fatalf("loaded count %d vs %d", len(a.Loaded), len(b.Loaded))
	}
}

func TestResolveIdempotentTwice(t *testing.T) {
	desired := desiredWithDisabledPack("painted-wolf/options")
	a, err := resolveWithDesired(t.Context(), desired, nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	b, err := resolveWithDesired(t.Context(), desired, nil)
	testutil.FailErr(t, "effective catalog resolve failed", err)
	if a.Revision != b.Revision {
		t.Fatal("same desired must be byte-identical effective revision")
	}
}

func TestPackEnabledRequiresDesiredIntentForExtensions(t *testing.T) {
	if PackEnabled(EmptyDesired(), "acme/cached") {
		t.Fatal("unlisted cache pack must be inactive")
	}
	if !PackEnabled(EmptyDesired(), "painted-wolf/platform") {
		t.Fatal("stock pack must remain active by default")
	}
	if !PackEnabled(desiredWithExtensionPacks("acme/cached"), "acme/cached") {
		t.Fatal("listed extension pack must be active")
	}
}

func TestResolveExtensionAPIIncompatibleDropsPack(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "epoch-bad", Manifest{
		ID: "test/epoch-bad", Name: "bad",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^99.0.0"},
	}, map[string]string{
		"policy/EPOCH_ONLY.yaml": "id: EPOCH_ONLY\nemit: banner\nmessage: x\n",
	})
	platform := mustStockPack(t, "painted-wolf/platform")
	pcBad, err := InventoryPack(Pack{ID: "test/epoch-bad", Root: OnDisk(filepath.Join(dir, "epoch-bad"))}, Manifest{
		ID: "test/epoch-bad", Version: "1.0.0", Compatibility: ManifestCompatibility{ExtensionAPI: "^99.0.0"},
	})
	testutil.FailErr(t, "InventoryPack failed", err)
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{platform, pcBad},
		Desired: desiredWithExtensionPacks("test/epoch-bad"),
	})
	if eff.HasLoaded("policy/EPOCH_ONLY") {
		t.Fatal("extension-API-incompatible pack must not provide")
	}
	found := false
	for _, d := range eff.Diagnostics {
		if d.Code == DiagExtensionAPIIncompatible && d.PackID == "test/epoch-bad" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected extension_api_incompatible diagnostic")
	}
}

func TestResolveRequiresUnmetPackContributesNothing(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "needs-missing", Manifest{
		ID: "test/needs-missing", Name: "needs",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		Dependencies:  map[string]DependencyRequest{"test/does-not-exist": {Version: "*"}},
	}, map[string]string{
		"policy/NEEDS_X.yaml": "id: NEEDS_X\nemit: banner\nmessage: x\n",
	})
	man := Manifest{
		ID: "test/needs-missing", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"}, Dependencies: map[string]DependencyRequest{"test/does-not-exist": {Version: "*"}},
	}
	pc, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(filepath.Join(dir, "needs-missing"))}, man)
	testutil.FailErr(t, "InventoryPack failed", err)
	platform := mustStockPack(t, "painted-wolf/platform")
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{platform, pc},
		Desired: desiredWithExtensionPacks("test/scan-hard"),
	})
	if eff.HasLoaded("policy/NEEDS_X") {
		t.Fatal("unmet requires must contribute nothing")
	}
}

func TestResolveRequiresScannersUnmet(t *testing.T) {
	dir := t.TempDir()
	man := Manifest{
		ID: "test/scan-hard", Name: "scan",
		Compatibility:    ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		Dependencies:     map[string]DependencyRequest{"painted-wolf/platform": {Version: "*"}},
		RequiresScanners: []string{"lycaon-sast"},
	}
	writeFixturePack(t, dir, "scan-hard", man, map[string]string{
		"policy/SCAN_HARD.yaml": "id: SCAN_HARD\nemit: banner\nmessage: x\n",
	})
	pc, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(filepath.Join(dir, "scan-hard"))}, man)
	testutil.FailErr(t, "InventoryPack failed", err)
	platform := mustStockPack(t, "painted-wolf/platform")

	// Nil checker fails closed.
	effNil := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{platform, pc},
		Desired: desiredWithExtensionPacks("test/scan-hard"),
	})
	if effNil.HasLoaded("policy/SCAN_HARD") {
		t.Fatal("nil scanner checker must fail closed")
	}

	effOff := Resolve(t.Context(), ResolveInput{
		Packs:    []PackContent{platform, pc},
		Desired:  desiredWithExtensionPacks("test/scan-hard"),
		Scanners: mapScannerChecker{},
	})
	if effOff.HasLoaded("policy/SCAN_HARD") {
		t.Fatal("requires_scanners unmet must contribute nothing")
	}
	found := false
	for _, d := range effOff.Diagnostics {
		if d.Code == DiagRequiresScannersUnmet && d.ScannerID == "lycaon-sast" {
			found = true
		}
	}
	if !found {
		t.Fatal("diagnostic must name scanner id")
	}
	var blocked PackSummary
	for _, s := range effOff.Packs {
		if s.ID == man.ID {
			blocked = s
		}
	}
	if blocked.BlockedReason != BlockedRequiresScanners {
		t.Fatalf("blocked_reason = %q", blocked.BlockedReason)
	}
	if len(blocked.UnmetRequiresScanners) != 1 || blocked.UnmetRequiresScanners[0] != "lycaon-sast" {
		t.Fatalf("unmet = %v", blocked.UnmetRequiresScanners)
	}

	effOn := Resolve(t.Context(), ResolveInput{
		Packs:    []PackContent{platform, pc},
		Desired:  desiredWithExtensionPacks("test/scan-hard"),
		Scanners: mapScannerChecker{"lycaon-sast": true},
	})
	if !effOn.HasLoaded("policy/SCAN_HARD") {
		t.Fatal("requires_scanners met must contribute")
	}
}

// A pack whose tool profile allowlists an MCP tool still contributes with MCP
// off: availability is a per-contribution requirement join, never a pack gate.
func TestResolveProfileMCPAllowlistStillProvides(t *testing.T) {
	dir := t.TempDir()
	man := Manifest{
		ID: "test/mcp-soft", Name: "soft",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		Dependencies:  map[string]DependencyRequest{"painted-wolf/platform": {Version: "*"}},
	}
	writeFixturePack(t, dir, "mcp-soft", man, map[string]string{
		"policy/MCP_SOFT.yaml": "id: MCP_SOFT\nemit: banner\nmessage: x\n",
		"tools/profiles.yaml":  "profiles:\n  mcp_github_star: true\n",
	})
	pc, err := InventoryPack(Pack{ID: man.ID, Root: OnDisk(filepath.Join(dir, "mcp-soft"))}, man)
	testutil.FailErr(t, "InventoryPack failed", err)
	platform := mustStockPack(t, "painted-wolf/platform")
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{platform, pc},
		Desired: desiredWithExtensionPacks("test/mcp-soft"),
	})
	if !eff.HasLoaded("policy/MCP_SOFT") {
		t.Fatal("an MCP tool profile must not gate the pack")
	}
}

func TestResolveOwnPicksWinner(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "stock-plan", Manifest{
		ID: "painted-wolf/plan-fixture", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{
		"workflows/plan/workflow.yaml": "id: plan\nversion: \"1.0.0\"\nname: stock\n",
	})
	writeFixturePack(t, dir, "acme-plan", Manifest{
		ID: "acme/plan", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{
		"workflows/plan/workflow.yaml": "id: plan\nversion: \"1.0.0\"\nname: acme\n",
	})
	a, err := InventoryPack(Pack{ID: "painted-wolf/plan-fixture", Root: OnDisk(filepath.Join(dir, "stock-plan"))}, Manifest{
		ID: "painted-wolf/plan-fixture", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack failed", err)
	b, err := InventoryPack(Pack{ID: "acme/plan", Root: OnDisk(filepath.Join(dir, "acme-plan"))}, Manifest{
		ID: "acme/plan", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack failed", err)
	desired := EmptyDesired()
	desired.Packs = []DesiredPack{{ID: "acme/plan"}}
	desired.Own = map[string]string{WorkflowUnitID("plan"): "acme/plan"}
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{a, b},
		Desired: desired,
	})
	u, ok := eff.Loaded[WorkflowUnitID("plan")]
	if !ok || u.Status != UnitStatusOwned || u.WinnerPackID != "acme/plan" {
		t.Fatalf("expected own selection for acme, got %+v ok=%v", u, ok)
	}
	if !strings.Contains(string(u.Content), "acme") {
		t.Fatalf("expected ACME body, got %q", u.Content)
	}
}

func TestResolveConflictNotLoaded(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "p1", Manifest{
		ID: "test/p1", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{"policy/DUP.yaml": "id: DUP\nemit: banner\nmessage: one\n"})
	writeFixturePack(t, dir, "p2", Manifest{
		ID: "test/p2", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{"policy/DUP.yaml": "id: DUP\nemit: banner\nmessage: two\n"})
	a, _ := InventoryPack(Pack{ID: "test/p1", Root: OnDisk(filepath.Join(dir, "p1"))}, Manifest{
		ID: "test/p1", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	b, _ := InventoryPack(Pack{ID: "test/p2", Root: OnDisk(filepath.Join(dir, "p2"))}, Manifest{
		ID: "test/p2", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{a, b},
		Desired: desiredWithExtensionPacks("test/p1", "test/p2"),
	})
	if eff.HasLoaded("policy/DUP") {
		t.Fatal("conflict must not load")
	}
	u := eff.Units["policy/DUP"]
	if u.Status != UnitStatusConflict {
		t.Fatalf("status=%s want conflict", u.Status)
	}
	if len(eff.InspectContributions("policy/DUP")) != 2 {
		t.Fatal("inspect must keep both contribution bodies")
	}
}

func TestMergeDesiredAddsProjectDisables(t *testing.T) {
	tr := true
	device := DesiredState{
		Format: DesiredFormat,
		Packs: []DesiredPack{
			{ID: "painted-wolf/plan", Enabled: &tr, Ref: "device"},
		},
		Disabled: []string{"policy/A"},
		Own:      map[string]string{"workflows/plan": "device/pack"},
	}
	m, _ := MergeDesired(device, []string{"policy/B"})
	if !PackEnabled(m, "painted-wolf/plan") {
		t.Fatal("a project must not disable a device pack")
	}
	if m.Own["workflows/plan"] != "device/pack" {
		t.Fatalf("own=%q want device", m.Own["workflows/plan"])
	}
	if len(m.Disabled) != 2 {
		t.Fatalf("disabled union want 2 got %v", m.Disabled)
	}
}

func TestLoadDesiredFileRejectsUnsupportedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extensions.yaml")
	err := os.WriteFile(path, []byte("format: 99\n"), 0o644)
	testutil.FailErr(t, "write desired fixture", err)

	_, err = LoadDesiredFile(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported extensions.yaml format 99") {
		t.Fatalf("LoadDesiredFile error = %v", err)
	}
}

func TestLoadDesiredFileRejectsMultipleDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extensions.yaml")
	err := os.WriteFile(path, []byte("format: 1\n---\nformat: 1\n"), 0o600)
	testutil.FailErr(t, "write desired", err)
	_, err = LoadDesiredFile(path)
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("err=%v", err)
	}
}

func writeFixturePack(t *testing.T, root, leaf string, man Manifest, files map[string]string) {
	t.Helper()
	packRoot := filepath.Join(root, leaf)
	if err := os.MkdirAll(packRoot, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	data := mustYAML(t, man)
	if err := os.WriteFile(filepath.Join(packRoot, "extension.yaml"), data, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if filepath.Base(packRoot) == PackIDSafe(man.ID) {
		testutil.FailErr(t, "write package body metadata", WritePackageBodyMetadata(packRoot, PackageBodyMetadata{
			PackID: man.ID,
			Kind:   PackKindGit,
		}))
	}
	for rel, body := range files {
		path := filepath.Join(packRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
}

func mustYAML(t *testing.T, man Manifest) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("manifest_version: 1\n")
	b.WriteString("id: " + man.ID + "\n")
	if man.Name != "" {
		b.WriteString("name: " + man.Name + "\n")
	}
	version := man.Version
	if version == "" {
		version = "1.0.0"
	}
	b.WriteString("version: \"" + version + "\"\n")
	api := man.Compatibility.ExtensionAPI
	if api == "" {
		api = "^1.0.0"
	}
	b.WriteString("compatibility:\n  extension_api: \"" + api + "\"\n")
	if len(man.Dependencies) > 0 {
		b.WriteString("dependencies:\n")
		for _, id := range man.DependencyIDs() {
			request := man.Dependencies[id]
			b.WriteString("  " + id + ":\n")
			if request.Source != "" {
				b.WriteString("    source: " + request.Source + "\n")
			}
			b.WriteString("    version: \"" + request.Version + "\"\n")
		}
	}
	if len(man.RequiresScanners) > 0 {
		b.WriteString("requires_scanners:\n")
		for _, r := range man.RequiresScanners {
			b.WriteString("  - " + r + "\n")
		}
	}
	return []byte(b.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func mustStockPack(t *testing.T, id string) PackContent {
	t.Helper()
	all, err := DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent failed", err)
	for _, pc := range all {
		if pc.Pack.ID == id {
			return pc
		}
	}
	t.Fatalf("stock pack %s not found", id)
	return PackContent{}
}
