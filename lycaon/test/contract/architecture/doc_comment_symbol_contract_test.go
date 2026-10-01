package contract

import (
	"fmt"
	"go/ast"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// docSubjectExceptions lists declarations whose docs lead with another subject.
var docSubjectExceptions = map[string]string{
	"internal/llm/providers/vertexexpress/vertex_express_wire_test.go#TestTokenUsageFromVertexExpress":             "leads with the promptTokenCount wire field it decodes",
	"test/contract/security/balanced_boundary_invariants_test.go#TestInvariantWriteRootSSOTSharedByGateAndConfine": "leads with WriteRootsForProject, the shared symbol under test",
}

// TestDocCommentNamesItsSymbol rejects docs that begin with another symbol.
// Only identifier-like subjects count. A contained name is not a
// mismatch. Generated files are skipped: their headers name the generator.
func TestDocCommentNamesItsSymbol(t *testing.T) {
	t.Parallel()
	corp, err := contractcheck.LoadGoASTCorpus(configlayout.FindModuleRoot())
	contractcheck.FailErr(t, "load AST corpus", err)

	var hits []string
	for _, f := range corp.Files() {
		if isGeneratedGoFile(f.AST) {
			continue
		}
		for _, decl := range f.AST.Decls {
			name, doc := declNameAndDoc(decl)
			if name == "" || doc == nil {
				continue
			}
			subject := docLeadIdentifier(doc)
			if subject == "" || subject == name || strings.Contains(name, subject) {
				continue
			}
			if _, ok := docSubjectExceptions[f.Rel+"#"+name]; ok {
				continue
			}
			hits = append(hits, fmt.Sprintf("%s:%d: doc opens with %q but declares %q",
				f.Rel, corp.Fset.Position(decl.Pos()).Line, subject, name))
		}
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("doc comment names a symbol other than its declaration (%d found).\n"+
			"Rename the doc to match, or move the stranded line to the symbol it documents —\n"+
			"that symbol is usually undocumented in the same file. A doc that genuinely leads\n"+
			"with its subject goes in docSubjectExceptions with the reason.\n  %s",
			len(hits), strings.Join(hits, "\n  "))
	}
}

// declNameAndDoc returns the declared name and doc comment for a func, or for a
// type/const/var declaration that declares exactly one name. A parenthesized
// group's doc describes the group, not any one member, so it is skipped.
func declNameAndDoc(decl ast.Decl) (string, *ast.CommentGroup) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Name == nil {
			return "", nil
		}
		return d.Name.Name, d.Doc
	case *ast.GenDecl:
		if d.Lparen.IsValid() || len(d.Specs) != 1 {
			return "", nil
		}
		switch s := d.Specs[0].(type) {
		case *ast.TypeSpec:
			return s.Name.Name, d.Doc
		case *ast.ValueSpec:
			if len(s.Names) != 1 {
				return "", nil
			}
			return s.Names[0].Name, d.Doc
		}
	}
	return "", nil
}

var docLeadHump = regexp.MustCompile(`[a-z][A-Z]`)

// docLeadIdentifier returns the doc's first word when it is unmistakably a Go
// identifier, and "" otherwise.
func docLeadIdentifier(doc *ast.CommentGroup) string {
	text := strings.TrimSpace(doc.Text())
	if text == "" {
		return ""
	}
	word := strings.TrimRight(strings.Fields(text)[0], ":,.")
	if len(word) < 8 || !isGoIdentifier(word) {
		return ""
	}
	if strings.ToLower(word) == word {
		return ""
	}
	if len(docLeadHump.FindAllString(word, -1)) < 2 && !strings.Contains(word, "_") {
		return ""
	}
	return word
}

func isGoIdentifier(s string) bool {
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// isGeneratedGoFile reports the standard `Code generated … DO NOT EDIT.` header.
func isGeneratedGoFile(f *ast.File) bool {
	for _, group := range f.Comments {
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, "// Code generated ") && strings.HasSuffix(c.Text, "DO NOT EDIT.") {
				return true
			}
		}
		if group.End() > f.Package {
			return false
		}
	}
	return false
}
