package contract

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type layeringRule struct {
	name         string
	fromGlob     string
	forbidden    string
	allowImports []string
	why          string
}

func layeringRules() []layeringRule {
	const modulePath = "github.com/lycaon/lycaon"
	return []layeringRule{
		{
			name:      "pkg/api is the wire surface — no internal deps",
			fromGlob:  modulePath + "/pkg/api",
			forbidden: modulePath + "/internal/",
			why:       "pkg/api ships in the public wire types; importing internal would couple consumers to internals",
		},
		{
			name:      "internal/* must not import internal/api",
			fromGlob:  modulePath + "/internal/",
			forbidden: modulePath + "/internal/api",
			why:       "internal/api is the HTTP layer; lower packages must not reach back into the handler tree",
		},
		{
			name:      "internal/db is the bottom — no internal cross-deps",
			fromGlob:  modulePath + "/internal/db",
			forbidden: modulePath + "/internal/",
			// DB imports only shared storage leaves.
			allowImports: []string{
				modulePath + "/internal/configdir",
				modulePath + "/internal/fssync",
				modulePath + "/internal/timelayout",
			},
			why: "db is the SQL adapter; depending on other internal/* packages would create cycles via every storage user",
		},
		{
			name:      "prompts must not import tools hub",
			fromGlob:  modulePath + "/internal/prompts",
			forbidden: modulePath + "/internal/tools",
			// Prompts imports shared tool-policy leaves.
			allowImports: []string{
				modulePath + "/internal/tools/readcaps",
				modulePath + "/internal/tools/surveyreceipt",
				modulePath + "/internal/toolcontract",
			},
			why: "prompts → tools hub would cycle via guidance; shared facts live in pure leaves",
		},
		{
			name:      "pkg/api must not import internal/api",
			fromGlob:  modulePath + "/pkg/api",
			forbidden: modulePath + "/internal/api",
			why:       "wire types must not depend on the HTTP handler package",
		},
		{
			name:      "extpacks must not import contribution or catalogview",
			fromGlob:  modulePath + "/internal/extpacks",
			forbidden: modulePath + "/internal/contribution",
			why:       "the resolver selects opaque winning unit bytes; contribution semantics compile after resolution",
		},
		{
			name:      "extpacks must not import catalogview",
			fromGlob:  modulePath + "/internal/extpacks",
			forbidden: modulePath + "/internal/catalogview",
			why:       "view assembly consumes the resolver, never the reverse",
		},
		{
			name:      "contribution is a pure leaf — no internal deps",
			fromGlob:  modulePath + "/internal/contribution",
			forbidden: modulePath + "/internal/",
			// Contribution compiles against shared language and theme vocabularies.
			allowImports: []string{
				modulePath + "/internal/filekind",
				modulePath + "/internal/theme",
			},
			why: "contribution provides pure parsing/validation and the immutable compiled set; filesystem, resolver, and wire access live in their respective packages",
		},
		{
			name:      "extpacks must not import extensionstate",
			fromGlob:  modulePath + "/internal/extpacks",
			forbidden: modulePath + "/internal/extensionstate",
			why:       "the resolver and state parsers feed the subsystem owner, never the reverse",
		},
		{
			name:      "catalogview must not import extensionstate",
			fromGlob:  modulePath + "/internal/catalogview",
			forbidden: modulePath + "/internal/extensionstate",
			why:       "view assembly validates candidates for the operation; a reverse edge would cycle",
		},
		{
			name:      "catalogview must not import usernotice",
			fromGlob:  modulePath + "/internal/catalogview",
			forbidden: modulePath + "/internal/usernotice",
			why:       "notice loading is a separate consumer of the catalog; folding it into the view would put user-facing copy behind the view LRU",
		},
	}
}

func TestImportGraphLayering(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	moduleRoot := filepath.Join(root, "lycaon")
	const modulePath = "github.com/lycaon/lycaon"

	rules := layeringRules()

	// Loader hubs cannot compose back into catalogview.
	for _, hub := range []string{
		"hintregistry", "approvalregistry", "orchestration", "sandbox",
		"prompts", "oar", "coordinator/anchor", "toolschema", "mcp/bindings",
	} {
		rules = append(rules, layeringRule{
			name:      hub + " must not import catalogview",
			fromGlob:  modulePath + "/internal/" + hub,
			forbidden: modulePath + "/internal/catalogview",
			why:       "catalogview assembles this loader; composing back up would cycle",
		})
	}

	importsByFile := scanGoImports(t, moduleRoot)

	type violation struct {
		rule string
		file string
		imp  string
		why  string
	}
	var hits []violation

	for path, imports := range importsByFile {
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			continue
		}
		pkgRel := filepath.ToSlash(filepath.Dir(rel))
		pkgPath := modulePath + "/" + pkgRel

		for _, rule := range rules {
			if !packagePathMatches(pkgPath, rule.fromGlob) {
				continue
			}
			for _, imp := range imports {
				if imp == pkgPath {
					continue
				}
				if packagePathMatches(imp, rule.fromGlob) {
					continue
				}
				// The composition root provides HTTP wiring.
				if rule.forbidden == modulePath+"/internal/api" &&
					strings.HasPrefix(pkgPath, modulePath+"/internal/app") {
					continue
				}
				if !importMatchesForbidden(imp, rule.forbidden) {
					continue
				}
				// A trailing slash selects descendants only.
				if !strings.HasSuffix(rule.forbidden, "/") && packagePathMatches(pkgPath, rule.forbidden) {
					continue
				}
				if importAllowlisted(imp, rule.allowImports) {
					continue
				}
				hits = append(hits, violation{
					rule: rule.name,
					file: rel,
					imp:  imp,
					why:  rule.why,
				})
			}
		}
	}

	if len(hits) > 0 {
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].rule != hits[j].rule {
				return hits[i].rule < hits[j].rule
			}
			if hits[i].file != hits[j].file {
				return hits[i].file < hits[j].file
			}
			return hits[i].imp < hits[j].imp
		})
		var b strings.Builder
		b.WriteString("import graph layering violations:\n")
		var lastRule string
		for _, v := range hits {
			if v.rule != lastRule {
				b.WriteString("\n[")
				b.WriteString(v.rule)
				b.WriteString("] (")
				b.WriteString(v.why)
				b.WriteString(")\n")
				lastRule = v.rule
			}
			b.WriteString("  ")
			b.WriteString(v.file)
			b.WriteString(" → ")
			b.WriteString(v.imp)
			b.WriteString("\n")
		}
		t.Fatal(b.String())
	}
}

func scanGoImports(t *testing.T, root string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		var imps []string
		for _, imp := range file.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			imps = append(imps, value)
		}
		out[path] = imps
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	return out
}

// packagePathMatches reports whether pkgPath is within the prefix described
// by glob. A glob ending in "/" matches any sub-path; otherwise exact match.
func packagePathMatches(pkgPath, glob string) bool {
	if strings.HasSuffix(glob, "/") {
		return strings.HasPrefix(pkgPath, glob)
	}
	return pkgPath == glob || strings.HasPrefix(pkgPath, glob+"/")
}

func importAllowlisted(imp string, allow []string) bool {
	for _, a := range allow {
		if imp == a {
			return true
		}
	}
	return false
}

// importMatchesForbidden matches exact packages and descendants.
func importMatchesForbidden(imp, forbidden string) bool {
	if strings.HasSuffix(forbidden, "/") {
		return strings.HasPrefix(imp, forbidden)
	}
	return imp == forbidden || strings.HasPrefix(imp, forbidden+"/")
}
