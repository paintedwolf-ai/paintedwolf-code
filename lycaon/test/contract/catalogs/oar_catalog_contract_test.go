package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var verdictFactName = regexp.MustCompile(`(?i)(_violation|_is_ungrounded|_verdict|_decision_code|reject_code)$`)

func TestOARNoMaybeRejectFuncs(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	fset := token.NewFileSet()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil {
				continue
			}
			name := fn.Name.Name
			if strings.HasPrefix(name, "MaybeReject") {
				rel, _ := filepath.Rel(filepath.Join(contractcheck.RepoRoot(t), "lycaon"), path)
				hits = append(hits, rel+":"+name)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal/", err)
	if len(hits) > 0 {
		t.Fatalf("MaybeReject* helpers must publish observations and leave decisions to OAR:\n%s", strings.Join(hits, "\n"))
	}
}

func TestOARHintCoverageUsesCatalogAnchors(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	hintsAt := extpacks.Bundled(hintregistry.DefaultDir)
	schemaDir := filepath.Join(root, "schemas")
	loader, err := oar.NewLoader(schemaDir)
	contractcheck.FailErr(t, "oar loader", err)
	rs, err := loader.LoadDir(hintsAt)
	contractcheck.FailErr(t, "load OAR rules", err)
	cfg, err := guidance.LoadHintConfig(hintsAt)
	contractcheck.FailErr(t, "load hint registry", err)

	var missing []string
	var noRule []string
	var unknownAnchors []string
	for code, entry := range cfg.HintCodes {
		emit := strings.TrimSpace(entry.Emit)
		if !strings.HasPrefix(emit, "guard:") && !strings.HasPrefix(emit, "rule:") {
			continue
		}
		anchor := strings.TrimSpace(entry.Anchor)
		kind := strings.TrimSpace(entry.Kind)
		if anchor == "" || kind == "" {
			missing = append(missing, code)
			continue
		}
		if _, ok := rs.Get(code); !ok {
			noRule = append(noRule, code)
		}
		resolved, err := oar.ResolveRuleAnchor(code, anchor)
		if err != nil {
			unknownAnchors = append(unknownAnchors, code+"="+anchor)
			continue
		}
		if !anchorcatalog.Has(resolved) {
			unknownAnchors = append(unknownAnchors, code+"="+anchor)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("hints missing anchor or kind:\n%s", strings.Join(missing, "\n"))
	}
	if len(noRule) > 0 {
		t.Fatalf("hints with anchor and kind but not loaded as OAR rules:\n%s", strings.Join(noRule, "\n"))
	}
	if len(unknownAnchors) > 0 {
		t.Fatalf("unknown OAR anchors:\n%s", strings.Join(unknownAnchors, "\n"))
	}
}

func TestOARNoVerdictFacts(t *testing.T) {
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "oar")
	for _, name := range []string{"context.go", "env.go", "facts.go"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		contractcheck.FailErr(t, "read "+name, err)
		for _, line := range strings.Split(string(body), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "//") {
				continue
			}
			if verdictFactName.MatchString(trim) && (strings.Contains(trim, "`") || strings.Contains(trim, "\"")) {
				t.Fatalf("%s has verdict-shaped fact name: %s", name, trim)
			}
		}
	}
}

func TestOARObservationOnlyFacts(t *testing.T) {
	t.Parallel()
	TestOARNoVerdictFacts(t)

	declared := map[string]bool{}
	for _, f := range oar.FactCatalogue() {
		declared[f.Name] = true
	}
	for _, name := range []string{"command_not_argv", "is_directory", "reject_observation"} {
		if !declared[name] {
			t.Errorf("catalogue missing observation fact %q", name)
		}
	}
}
