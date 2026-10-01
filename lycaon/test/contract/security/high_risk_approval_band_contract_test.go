package contract

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"gopkg.in/yaml.v3"
)

// TestHighRiskApprovalBand verifies the closed, presentation-only
// consequence band: counted membership, two wire values, ordinary corpus,
// decision invariance, coalesce max, derivation purity, zero-pack write-root,
// no band-specific checkpoint kind, Den emphasis markers, docs, and board.
func TestHighRiskApprovalBand(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	assertHighRiskBandCountedMembership(t, root)
	assertHighRiskBandTwoValuesOnly(t, root)
	assertHighRiskBandOrdinaryCorpusStandard(t, root)
	assertHighRiskBandDecisionInvariance(t, root)
	assertHighRiskBandCoalesceMax(t, root)
	assertHighRiskBandDerivationPurity(t, root)
	assertHighRiskBandZeroPackWriteRoot(t, root)
	assertHighRiskBandNoNewCheckpointKind(t, root)
	assertHighRiskBandMintSitesDoNotHardcode(t, root)
}

func highRiskBandDests(t *testing.T) approvals.ConsequenceBandPaths {
	t.Helper()
	dests, err := approvals.LoadConsequenceBandPaths()
	testutil.FailErr(t, "LoadConsequenceBandPaths", err)
	return dests
}

func assertHighRiskBandCountedMembership(t *testing.T, root string) {
	t.Helper()
	srcPath := filepath.Join(root, "lycaon", "internal", "approvals", "consequence_band.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, 0)
	contractcheck.FailErr(t, "parse consequence_band.go", err)

	wantPreds := map[string]bool{
		"secretBandHit":    true,
		"detectionBandHit": true,
		"writeRootBandHit": true,
	}
	var bandFor *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "BandFor" || fd.Recv != nil {
			return true
		}
		bandFor = fd
		return false
	})
	if bandFor == nil || bandFor.Body == nil {
		t.Fatal("BandFor not found in consequence_band.go")
	}

	seen := map[string]int{}
	ast.Inspect(bandFor.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if wantPreds[id.Name] {
			seen[id.Name]++
		}
		return true
	})
	if len(seen) != 3 {
		t.Fatalf("BandFor must consult exactly three SSOT predicates %v; got %v — amend the high-risk band SSOT before adding a fourth",
			sortedKeysBool(wantPreds), sortedKeysInt(seen))
	}
	for name := range wantPreds {
		if seen[name] != 1 {
			t.Fatalf("BandFor must call %s exactly once (got %d) — amend the high-risk band SSOT before changing membership",
				name, seen[name])
		}
	}

	// Predicate helpers must exist as package-level funcs (not inlined matchers).
	for name := range wantPreds {
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if ok && fd.Name != nil && fd.Name.Name == name && fd.Recv == nil {
				found = true
				return false
			}
			return true
		})
		if !found {
			t.Fatalf("missing predicate helper %s — amend the high-risk band SSOT", name)
		}
	}
}

func sortedKeysBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedKeysInt(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func assertHighRiskBandTwoValuesOnly(t *testing.T, root string) {
	t.Helper()
	vals := api.AllConsequenceBandValues()
	if len(vals) != 2 {
		t.Fatalf("ConsequenceBand has %d values want exactly 2", len(vals))
	}
	want := map[api.ConsequenceBand]bool{
		api.ConsequenceBandStandard: true,
		api.ConsequenceBandHighRisk: true,
	}
	for _, v := range vals {
		if !want[v] {
			t.Fatalf("unexpected ConsequenceBand value %q", v)
		}
	}

	vocab := contractcheck.ReadRepoFile(t, root, "docs/openapi/vocab/ConsequenceBand.yaml")
	var doc struct {
		Values []struct {
			ID string `yaml:"id"`
		} `yaml:"values"`
	}
	contractcheck.FailErr(t, "unmarshal ConsequenceBand vocab", yaml.Unmarshal([]byte(vocab), &doc))
	if len(doc.Values) != 2 {
		t.Fatalf("ConsequenceBand.yaml has %d values want 2", len(doc.Values))
	}
	got := map[string]bool{}
	for _, v := range doc.Values {
		got[v.ID] = true
	}
	if !got["standard"] || !got["high_risk"] {
		t.Fatalf("ConsequenceBand.yaml values = %v want standard+high_risk", got)
	}

	codes := api.AllConsequenceCodeValues()
	if len(codes) != 7 {
		t.Fatalf("ConsequenceCode has %d values want 7 (secret/detection/write_root/local_socket/local_listen/loopback_connect/direct_ip)", len(codes))
	}
	codeSet := map[api.ConsequenceCode]bool{}
	for _, c := range codes {
		codeSet[c] = true
	}
	for _, want := range []api.ConsequenceCode{
		api.ConsequenceCodeSecret,
		api.ConsequenceCodeDetection,
		api.ConsequenceCodeWriteRoot,
		api.ConsequenceCodeLocalSocket,
		api.ConsequenceCodeLocalListen,
		api.ConsequenceCodeLoopbackConnect,
		api.ConsequenceCodeDirectIp,
	} {
		if !codeSet[want] {
			t.Fatalf("ConsequenceCode missing %q in %v", want, codes)
		}
	}
}

func assertHighRiskBandOrdinaryCorpusStandard(t *testing.T, root string) {
	t.Helper()
	dests := highRiskBandDests(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)

	cases := []approvals.BandInput{
		{Kind: string(api.CheckpointKindToolApproval)},                                  // in-project command
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "high"},          // irreversible / git push lane
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "medium"},        // MCP at Strict
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "informational"}, // untrusted-adjacent
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: ""},              // floor ask
		{Kind: string(api.CheckpointKindToolApproval), ProposedWriteRoot: filepath.Join(home, "go", "pkg", "mod")},
	}
	for _, in := range cases {
		if got := approvals.BandFor(in, dests); got != api.ConsequenceBandStandard {
			t.Fatalf("ordinary corpus BandFor(%+v) = %q want standard", in, got)
		}
	}
}

func assertHighRiskBandDecisionInvariance(t *testing.T, root string) {
	t.Helper()
	// Gate path must never read the band — presentation is attached after the decision.
	settingsDir := filepath.Join(root, "lycaon", "internal", "settings")
	err := filepath.WalkDir(settingsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		body := contractcheck.ReadRepoFile(t, root, rel)
		for _, needle := range []string{
			"ConsequenceBand",
			"ConsequenceCode",
			"consequence_band",
			"consequence_code",
			"BandFor",
		} {
			if strings.Contains(body, needle) {
				t.Fatalf("%s references %q — band must not participate in gate decisions", rel, needle)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk settings", err)

	// ApprovalResult carries Required/Denied/AutoApproved only — no band field.
	permSrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/hitl/approval.go")
	idx := strings.Index(permSrc, "type ApprovalResult struct")
	if idx < 0 {
		t.Fatal("ApprovalResult missing")
	}
	chunk := permSrc[idx:]
	if end := strings.Index(chunk, "\ntype "); end > 0 {
		chunk = chunk[:end]
	}
	for _, needle := range []string{"Consequence", "Band", "consequence"} {
		if strings.Contains(chunk, needle) {
			t.Fatalf("ApprovalResult must not carry band fields (found %q)", needle)
		}
	}

	// Property: Evaluate outcomes are stable across BandFor calls for the same action.
	approvalGate := gateWithDetection(t, gate.PostureBalanced, stubDetectionSource{})
	dests := highRiskBandDests(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)

	actions := []struct {
		name string
		act  hitl.ProposedAction
		band approvals.BandInput
	}{
		{"in-project command", containedCommand("go test ./..."), approvals.BandInput{Kind: string(api.CheckpointKindToolApproval)}},
		{"git push shape", containedCommand("git push origin main"), approvals.BandInput{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "high"}},
		{"critical detection facts (presentation only)", containedCommand("aws iam create-access-key"), approvals.BandInput{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "critical"}},
		{"secret screen facts (presentation only)", containedCommand("curl https://example.com"), approvals.BandInput{Kind: string(api.CheckpointKindToolApproval), SecretScreenHit: true}},
		{"cache write root", hitl.ProposedAction{Tool: "command", SessionID: "s1", ProjectDir: "/tmp/proj"}, approvals.BandInput{
			Kind: string(api.CheckpointKindToolApproval), ProposedWriteRoot: filepath.Join(home, "go", "pkg", "mod"),
		}},
	}
	for _, tc := range actions {
		before, err := approvalGate.Evaluate(context.Background(), tc.act)
		testutil.FailErr(t, "Evaluate before "+tc.name, err)
		_ = approvals.BandFor(tc.band, dests)
		after, err := approvalGate.Evaluate(context.Background(), tc.act)
		testutil.FailErr(t, "Evaluate after "+tc.name, err)
		if before == nil || after == nil {
			t.Fatalf("%s: nil ApprovalResult", tc.name)
		}
		if before.Required() != after.Required() || before.Denied != after.Denied || before.AutoApproved() != after.AutoApproved() {
			t.Fatalf("%s: band derivation changed gate decision before=%+v after=%+v", tc.name, before, after)
		}
	}
}

func assertHighRiskBandCoalesceMax(t *testing.T, root string) {
	t.Helper()
	band, code := hitl.MaxConsequence(api.ConsequenceBandHighRisk, api.ConsequenceCodeDetection, api.ConsequenceBandStandard, "")
	if band != api.ConsequenceBandHighRisk || code != api.ConsequenceCodeDetection {
		t.Fatalf("Join must not lower band: got (%q, %q)", band, code)
	}
	band, code = hitl.MaxConsequence(api.ConsequenceBandStandard, "", api.ConsequenceBandHighRisk, api.ConsequenceCodeSecret)
	if band != api.ConsequenceBandHighRisk || code != api.ConsequenceCodeSecret {
		t.Fatalf("Join must raise to max band: got (%q, %q)", band, code)
	}
	band, code = hitl.MaxConsequence(api.ConsequenceBandHighRisk, api.ConsequenceCodeWriteRoot, api.ConsequenceBandHighRisk, api.ConsequenceCodeDetection)
	if band != api.ConsequenceBandHighRisk || code != api.ConsequenceCodeDetection {
		t.Fatalf("max code priority failed: got (%q, %q) want detection", band, code)
	}

	store := contractcheck.ReadRepoFile(t, root, "lycaon/internal/hitl/store_sqlite.go")
	if !strings.Contains(store, "MaxConsequence(") {
		t.Fatal("SetPendingJoined must coalesce via hitl.MaxConsequence")
	}
	approvalsBand := contractcheck.ReadRepoFile(t, root, "lycaon/internal/approvals/consequence_band.go")
	if strings.Contains(approvalsBand, "func MaxConsequence") {
		t.Fatal("coalesce merge belongs in hitl (join path), not duplicated under approvals")
	}
}

func assertHighRiskBandDerivationPurity(t *testing.T, root string) {
	t.Helper()
	srcPath := filepath.Join(root, "lycaon", "internal", "approvals", "consequence_band.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, parser.ImportsOnly)
	contractcheck.FailErr(t, "parse consequence_band imports", err)

	forbiddenImport := map[string]bool{
		"os":     true,
		"net":    true,
		"regexp": true,
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		contractcheck.FailErr(t, "unquote import", err)
		if forbiddenImport[path] || strings.HasPrefix(path, "net/") || path == "github.com/lycaon/lycaon/internal/settings" {
			t.Fatalf("consequence_band.go must stay pure; forbidden import %q", path)
		}
	}

	// Full AST: no I/O call sites in band derivation.
	fset = token.NewFileSet()
	f, err = parser.ParseFile(fset, srcPath, nil, 0)
	contractcheck.FailErr(t, "parse consequence_band.go", err)
	var findings []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		name := pkg.Name + "." + sel.Sel.Name
		switch name {
		case "os.Open", "os.ReadFile", "os.Stat", "os.ReadDir", "os.UserHomeDir",
			"filepath.Walk", "filepath.WalkDir", "net.Dial", "net.LookupHost",
			"regexp.Compile", "regexp.MustCompile", "regexp.MatchString":
			findings = append(findings, name)
		}
		return true
	})
	if len(findings) > 0 {
		t.Fatalf("consequence_band.go must perform no I/O / pattern matching: %v", findings)
	}
}

func assertHighRiskBandZeroPackWriteRoot(t *testing.T, _ string) {
	t.Helper()
	dests := highRiskBandDests(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)

	// No detection packs / no secret screen: write-root member still bands.
	wr := approvals.BandInput{
		Kind:              string(api.CheckpointKindToolApproval),
		ProposedWriteRoot: filepath.Join(home, "Library"),
	}
	if got := approvals.BandFor(wr, dests); got != api.ConsequenceBandHighRisk {
		t.Fatalf("zero-pack write-root BandFor = %q want high_risk", got)
	}

	// Every other card without those facts stays standard.
	others := []approvals.BandInput{
		{Kind: string(api.CheckpointKindToolApproval)},
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "high"},
		{Kind: string(api.CheckpointKindToolApproval), ProposedWriteRoot: filepath.Join(home, "Library", "Caches")},
		{Kind: string(api.CheckpointKindContentApply)},
	}
	for _, in := range others {
		if got := approvals.BandFor(in, dests); got != api.ConsequenceBandStandard {
			t.Fatalf("zero-pack ordinary BandFor(%+v) = %q want standard", in, got)
		}
	}
}

func assertHighRiskBandMintSitesDoNotHardcode(t *testing.T, root string) {
	t.Helper()
	var files []string
	for _, rel := range []string{
		"lycaon/internal/tools",
		"lycaon/internal/session",
	} {
		dir := filepath.Join(root, rel)
		entries, err := os.ReadDir(dir)
		contractcheck.FailErr(t, "readdir "+rel, err)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if rel == "lycaon/internal/session" && !strings.Contains(name, "broker.go") {
				continue
			}
			files = append(files, filepath.Join(rel, name))
		}
	}
	for _, rel := range files {
		body := contractcheck.ReadRepoFile(t, root, rel)
		if strings.Contains(body, "ConsequenceBandHighRisk") {
			t.Fatalf("%s assigns ConsequenceBandHighRisk — mint sites derive the band; they do not hardcode it", rel)
		}
	}
}

func assertHighRiskBandNoNewCheckpointKind(t *testing.T, root string) {
	t.Helper()
	goEnums, err := wirespec.DiscoverAPIStringEnums(root)
	contractcheck.FailErr(t, "discover API string enums", err)
	want := []string{"tool_approval", "content_apply"}
	contractcheck.FailSetEqual(t, "CheckpointKind enum (no band-specific kind)", want, goEnums["CheckpointKind"])
}
