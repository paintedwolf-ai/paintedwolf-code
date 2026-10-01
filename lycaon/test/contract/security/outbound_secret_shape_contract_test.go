package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every evidence source emits card-ready matches.
func assertEveryMatchCarriesAGenericShape(t *testing.T, root string) {
	t.Helper()
	scanned := 0
	for _, file := range []string{
		"lycaon/internal/secretmatch/secretmatch.go",
		"lycaon/internal/secretmatch/harvest_fusion.go",
	} {
		src := contractcheck.ReadRepoFile(t, root, file)
		for _, literal := range matchCompositeLiterals(src) {
			scanned++
			if !strings.Contains(literal, "GenericShape:") {
				t.Fatalf("%s builds a Match with no GenericShape; no approval card can be compiled from it:\n%s", file, literal)
			}
		}
	}
	// Require both evidence sources.
	if scanned < 2 {
		t.Fatalf("found %d populated Match literals across both evidence sources, want at least one each", scanned)
	}
}

// matchCompositeLiterals returns populated Match literal bodies.
func matchCompositeLiterals(src string) []string {
	var out []string
	for idx := 0; ; {
		at := strings.Index(src[idx:], "Match{")
		if at < 0 {
			return out
		}
		start := idx + at + len("Match{")
		depth := 1
		end := start
		for end < len(src) && depth > 0 {
			switch src[end] {
			case '{':
				depth++
			case '}':
				depth--
			}
			end++
		}
		body := src[start:end]
		if strings.Contains(body, ":") {
			out = append(out, body)
		}
		idx = end
	}
}

func assertOutboundSecretMatchHasNoValue(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join(root, "lycaon/internal/secretmatch/secretmatch.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	testutil.FailErr(t, "parse secretmatch.go", err)
	var matchFields []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Match" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				matchFields = append(matchFields, name.Name)
			}
		}
		return false
	})
	if len(matchFields) == 0 {
		t.Fatal("secretmatch.Match struct not found")
	}
	for _, name := range matchFields {
		switch strings.ToLower(name) {
		case "secret", "match", "line", "value", "raw":
			t.Fatalf("secretmatch.Match must not retain matched value field %q", name)
		}
	}
	// Provenance fields remain value-free.
	want := map[string]bool{
		"RuleID": true, "Title": true, "Severity": true,
		"Start": true, "End": true, "GenericShape": true,
		"Fingerprint": true, "vendor": true,
		"VarName": true, "Container": true, "Source": true,
		"Reference": true, "Retired": true, "NonDisclosable": true,
	}
	for _, name := range matchFields {
		if !want[name] {
			t.Fatalf("unexpected Match field %q", name)
		}
		delete(want, name)
	}
	for missing := range want {
		t.Fatalf("Match missing field %q", missing)
	}
	dir := filepath.Join(root, "lycaon/internal/secretmatch")
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read secretmatch", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw := contractcheck.ReadRepoFile(t, root, filepath.Join("lycaon/internal/secretmatch", e.Name()))
		if strings.Contains(raw, "type Finding struct") {
			t.Fatalf("%s must not export Finding", e.Name())
		}
		if strings.Contains(raw, "func ") && strings.Contains(raw, ") report.Finding") {
			t.Fatalf("%s must not return report.Finding", e.Name())
		}
	}
}

func assertOutboundSecretGitleaksNamespace(t *testing.T, root string) {
	t.Helper()
	sm := contractcheck.ReadRepoFile(t, root, "lycaon/internal/secretmatch/secretmatch.go")
	if !strings.Contains(sm, "gitleaks/v8/detect") {
		t.Fatal("secretmatch must import gitleaks/v8/detect")
	}
	if !strings.Contains(sm, `gitleaksPrefix = "gitleaks:"`) && !strings.Contains(sm, `"gitleaks:"`) {
		t.Fatal("secretmatch must use gitleaks: rule-id prefix")
	}
	needles := []string{"AKIA", "ghp_", "xox", "AIza", "sk-ant"}
	allowRel := func(rel string) bool {
		rel = filepath.ToSlash(rel)
		switch {
		case strings.Contains(rel, "/vendor/"),
			strings.Contains(rel, "gitleaks/"),
			strings.Contains(rel, "secret-patterns/"),
			strings.HasSuffix(rel, "/secret-placeholders.yaml"),
			strings.Contains(rel, "detection-packs/"),
			strings.Contains(rel, "/testdata/"),
			strings.HasSuffix(rel, "_test.go"),
			strings.Contains(rel, "/test/"),
			strings.Contains(rel, "/approvals/outbound_secret/"),
			strings.Contains(rel, "user-notices/outbound_secret"),
			strings.Contains(rel, "policy/OUTBOUND_SECRET_DENIED.yaml"):
			return true
		default:
			return false
		}
	}
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), ".yaml") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if allowRel(rel) {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		for _, n := range needles {
			if strings.Contains(text, n) {
				t.Errorf("%s contains credential-shape literal %q outside allowlisted rule sources", rel, n)
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk credential literals", err)
}
