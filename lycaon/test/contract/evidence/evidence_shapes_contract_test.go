package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestShapeIDsFrozen(t *testing.T) {
	t.Parallel()
	want := []string{
		evidence.ShapeFileRegion,
		evidence.ShapeURL,
		evidence.ShapeCommand,
		evidence.ShapeArtifact,
		evidence.ShapeOpaque,
		evidence.ShapeStructuredEvent,
		evidence.ShapeVisual,
		evidence.ShapeSurfaceSnapshot,
		evidence.ShapePageGeometry,
	}
	got := evidence.ShapeIDs()
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence.ShapeIDs = %v want %v", got, want)
	}
}

func TestEvidenceTrustTiersFrozen(t *testing.T) {
	t.Parallel()
	want := []string{
		evidence.FidelityStructured,
		evidence.FidelityScraped,
		evidence.FidelityOpaque,
	}
	got := evidence.EvidenceTrustTierIDs()
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EvidenceTrustTierIDs = %v want %v", got, want)
	}
}

func TestEvidenceVerbatimShapesIncludeCommandAndOpaque(t *testing.T) {
	t.Parallel()
	got := evidence.EvidenceVerbatimShapeIDs()
	sort.Strings(got)
	want := []string{evidence.ShapeCommand, evidence.ShapeOpaque, evidence.ShapeStructuredEvent}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EvidenceVerbatimShapeIDs = %v want %v", got, want)
	}
}

func TestEvidenceTrivialityFloorLocked(t *testing.T) {
	t.Parallel()
	if evidence.EvidenceMinMeaningfulSpan < 4 {
		t.Fatalf("evidence.EvidenceMinMeaningfulSpan = %d want a meaningful floor (≥4)", evidence.EvidenceMinMeaningfulSpan)
	}
}

func TestEvidenceCaptureCapLocked(t *testing.T) {
	t.Parallel()
	if evidence.EvidenceCaptureBodyCapBytes < 4096 {
		t.Fatalf("evidence.EvidenceCaptureBodyCapBytes = %d want a practical capture cap", evidence.EvidenceCaptureBodyCapBytes)
	}
	if evidence.VerifyReasonTruncated == "" {
		t.Fatal("evidence.VerifyReasonTruncated must be set for truncated capture transparency")
	}
}

func TestUndeclaredMCPDefaultsToOpaqueShape(t *testing.T) {
	t.Parallel()
	if evidence.DefaultUndeclaredMCPShape != evidence.ShapeOpaque {
		t.Fatalf("evidence.DefaultUndeclaredMCPShape = %q want %q", evidence.DefaultUndeclaredMCPShape, evidence.ShapeOpaque)
	}
}

func TestEvidenceKindShapeMapComplete(t *testing.T) {
	t.Parallel()
	if len(expectedEvidenceKindShapes) != 30 {
		t.Fatalf("expectedEvidenceKindShapes has %d kinds want 30", len(expectedEvidenceKindShapes))
	}
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	live := binding.KindShapeMap()
	if len(live) != len(expectedEvidenceKindShapes) {
		t.Fatalf("evidence_kinds len = %d want %d", len(live), len(expectedEvidenceKindShapes))
	}
	for kind, wantShape := range expectedEvidenceKindShapes {
		gotShape, ok := live[kind]
		if !ok {
			t.Fatalf("evidence_kinds missing kind %q", kind)
		}
		if gotShape != wantShape {
			t.Fatalf("kind %q shape = %q want %q", kind, gotShape, wantShape)
		}
	}
	for _, shape := range live {
		if !isLockedEvidenceShape(shape) {
			t.Fatalf("unexpected shape %q in kind map", shape)
		}
	}
}

func TestEvidenceKindShapeMapUsesOnlyLockedShapes(t *testing.T) {
	t.Parallel()
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	for kind, shape := range binding.KindShapeMap() {
		switch shape {
		case evidence.ShapeFileRegion, evidence.ShapeURL, evidence.ShapeCommand, evidence.ShapeArtifact,
			evidence.ShapeVisual, evidence.ShapeSurfaceSnapshot, evidence.ShapePageGeometry, evidence.ShapeStructuredEvent:
		default:
			t.Fatalf("kind %q maps to unexpected shape %q", kind, shape)
		}
	}
}

func TestSurfaceSnapshotShapeIsSingular(t *testing.T) {
	t.Parallel()
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)

	shapeKindCount := map[string]int{}
	for _, shape := range binding.KindShapeMap() {
		shapeKindCount[shape]++
	}
	if shapeKindCount[evidence.ShapeSurfaceSnapshot] < 1 {
		t.Fatal("expected at least one evidence_kinds entry with shape surface_snapshot")
	}
	if binding.KindShape("page") != evidence.ShapeSurfaceSnapshot {
		t.Fatalf("page kind shape = %q want %q", binding.KindShape("page"), evidence.ShapeSurfaceSnapshot)
	}
	if binding.KindShape("render") != evidence.ShapeVisual {
		t.Fatalf("render kind shape = %q want %q", binding.KindShape("render"), evidence.ShapeVisual)
	}
	if binding.ToolKind("capture_page") != "page" {
		t.Fatalf("capture_page kind = %q want page", binding.ToolKind("capture_page"))
	}
	if binding.ToolKind("page_snapshot") != "page" {
		t.Fatalf("page_snapshot kind = %q want page", binding.ToolKind("page_snapshot"))
	}
	if binding.KindShape("tui") != evidence.ShapeSurfaceSnapshot {
		t.Fatalf("tui kind shape = %q want %q", binding.KindShape("tui"), evidence.ShapeSurfaceSnapshot)
	}
	if binding.ToolKind("terminal_snapshot") != "terminal_session" {
		t.Fatalf("terminal_snapshot kind = %q want terminal_session", binding.ToolKind("terminal_snapshot"))
	}
	if binding.ToolKind("measure_page") != "page_geometry" {
		t.Fatalf("measure_page kind = %q want page_geometry", binding.ToolKind("measure_page"))
	}
	if binding.KindShape("page_geometry") != evidence.ShapePageGeometry {
		t.Fatalf("page_geometry kind shape = %q want %q", binding.KindShape("page_geometry"), evidence.ShapePageGeometry)
	}
	if binding.ToolKind("render_view") != "render" {
		t.Fatalf("render_view kind = %q want render", binding.ToolKind("render_view"))
	}

	fields := evidence.SurfaceSnapshotHandleFields()
	wantFields := []string{
		evidence.SurfaceSnapshotHandleFieldSurface,
		evidence.SurfaceSnapshotHandleFieldArtifactID,
		evidence.SurfaceSnapshotHandleFieldFrameIndex,
	}
	sort.Strings(fields)
	sort.Strings(wantFields)
	if !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("SurfaceSnapshotHandleFields = %v want %v", fields, wantFields)
	}
	match := 0
	for _, id := range evidence.ShapeIDs() {
		if id == evidence.ShapeSurfaceSnapshot {
			match++
		}
	}
	if match != 1 {
		t.Fatalf("ShapeIDs must contain surface_snapshot exactly once; got %d", match)
	}
	surfaces := evidence.SurfaceSnapshotSurfaces()
	wantSurfaces := []string{evidence.SurfaceDOM, evidence.SurfaceTUI, evidence.SurfaceHTTP}
	if !reflect.DeepEqual(surfaces, wantSurfaces) {
		t.Fatalf("SurfaceSnapshotSurfaces = %v want %v", surfaces, wantSurfaces)
	}
}

func TestHandleKindGrammarIsOpenString(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"read", "mykind", "custom"} {
		handle := evidence.FormatHandle(kind, 1)
		if !evidence.HandleGrammar.MatchString(handle) {
			t.Fatalf("HandleGrammar rejected open kind %q handle %q", kind, handle)
		}
	}
	if evidence.HandleGrammar.MatchString("BadKind#1") {
		t.Fatal("HandleGrammar must require lowercase kind segment")
	}
}

func TestEvidenceShapeVerifierInterfaceSignature(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "evidence", "shape.go")
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read shape.go", err)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, body, 0)
	testutil.FailErr(t, "parse shape.go", err)

	var verifierFound bool
	ast.Inspect(f, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name == nil || typeSpec.Name.Name != "Verifier" {
			return true
		}
		iface, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok || len(iface.Methods.List) != 1 {
			t.Fatal("Verifier interface must declare exactly one method")
		}
		method := iface.Methods.List[0]
		if len(method.Names) != 1 || method.Names[0].Name != "Verify" {
			t.Fatal("Verifier must declare Verify method")
		}
		fn, ok := method.Type.(*ast.FuncType)
		if !ok || fn.Params.NumFields() != 2 || fn.Results.NumFields() != 2 {
			t.Fatal("Verify must be (evidence.Record, evidence.Claim) (bool, string)")
		}
		verifierFound = true
		return false
	})
	if !verifierFound {
		t.Fatal("missing Verifier interface in shape.go")
	}

	var normalizerFound bool
	ast.Inspect(f, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name == nil || typeSpec.Name.Name != "Normalizer" {
			return true
		}
		iface, ok := typeSpec.Type.(*ast.InterfaceType)
		if !ok || len(iface.Methods.List) != 1 {
			t.Fatal("Normalizer interface must declare exactly one method")
		}
		if iface.Methods.List[0].Names[0].Name != "Normalize" {
			t.Fatal("Normalizer must declare Normalize method")
		}
		normalizerFound = true
		return false
	})
	if !normalizerFound {
		t.Fatal("missing Normalizer interface in shape.go")
	}
}

func TestEvidenceShapeRegistryPresent(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	registryPath := filepath.Join(root, "lycaon", "internal", "evidence", "shape_registry.go")
	if _, err := os.Stat(registryPath); err != nil {
		t.Fatalf("missing shape registry at %s", registryPath)
	}
	if evidence.DefaultShapeRegistry() == nil {
		t.Fatal("evidence.DefaultShapeRegistry must be initialized")
	}
}

func TestEvidenceKindRolesFromConfig(t *testing.T) {
	t.Parallel()
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	if !reflect.DeepEqual(binding.SurveyKinds(), sortedCopy(evidenceSurveyKinds)) {
		t.Fatalf("SurveyKinds = %v want %v", binding.SurveyKinds(), evidenceSurveyKinds)
	}
	if !reflect.DeepEqual(binding.MutationKinds(), sortedCopy(evidenceMutationKinds)) {
		t.Fatalf("MutationKinds = %v want %v", binding.MutationKinds(), evidenceMutationKinds)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestBindingToolKindsFromConfig(t *testing.T) {
	t.Parallel()
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	if binding.ToolKind("unknown_native_tool") != "" {
		t.Fatalf("undeclared native tool kind = %q want empty", binding.ToolKind("unknown_native_tool"))
	}
	if binding.ToolKind("read") != "read" {
		t.Fatalf("read kind = %q want read", binding.ToolKind("read"))
	}
}

// TestDeclaredEvidenceToolsHaveCaptureCase checks every declared producer.
func TestDeclaredEvidenceToolsHaveCaptureCase(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)

	captured := captureSwitchToolNames(t, filepath.Join(root, "lycaon", "internal", "evidence", "fingerprint.go"))
	for _, tool := range binding.DeclaredToolNames() {
		if ingestion.IsMCPToolName(tool) {
			continue
		}
		if _, ok := captured[tool]; !ok {
			t.Fatalf("native evidence tool %q is declared in evidence-kinds.yaml but has no capture case in BuildEvidenceRecord — its records would be contentless", tool)
		}
	}
}

// captureSwitchToolNames returns the string case labels of the tool-name switch in
// BuildEvidenceRecord, parsed from source.
func captureSwitchToolNames(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	src, err := os.ReadFile(path)
	testutil.FailErr(t, "read fingerprint.go", err)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	testutil.FailErr(t, "parse fingerprint.go", err)

	out := map[string]struct{}{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "buildEvidenceRecord" {
			return true
		}
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			clause, ok := inner.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if name, err := strconv.Unquote(lit.Value); err == nil {
					out[name] = struct{}{}
				}
			}
			return true
		})
		return false
	})
	if len(out) == 0 {
		t.Fatal("found no case labels in BuildEvidenceRecord switch")
	}
	return out
}

func isLockedEvidenceShape(shape string) bool {
	for _, id := range evidence.ShapeIDs() {
		if id == shape {
			return true
		}
	}
	return false
}

func TestShapeIDsAreSnakeCaseOpenStrings(t *testing.T) {
	t.Parallel()
	for _, id := range evidence.ShapeIDs() {
		if strings.TrimSpace(id) == "" {
			t.Fatal("shape id must not be empty")
		}
		if strings.Contains(id, " ") {
			t.Fatalf("shape id %q must not contain spaces", id)
		}
	}
}
