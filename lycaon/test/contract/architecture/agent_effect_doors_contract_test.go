package contract

import (
	"go/ast"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// agentReachablePackages run on an agent's behalf: native commands, their
// HTTP owner, the program evaluator, tool execution and web research.
var agentReachablePackages = append([]string{"internal/toolexecution/", "internal/webresearch/"}, agentAdapterPackages...)

// agentForbiddenCalls bypass the launch and network doors: a host launch
// carries no agent confinement, os/exec starts a process outside the launch
// plan, and the default HTTP client dials without destination admission.
var agentForbiddenCalls = map[string]map[string]bool{
	"github.com/lycaon/lycaon/internal/exec": {"HostLaunch": true},
	"os/exec":                                {"Command": true, "CommandContext": true},
	"net/http":                               {"Get": true, "Head": true, "Post": true, "PostForm": true, "NewRequest": true, "NewRequestWithContext": true},
}

// agentForbiddenImports hold credentials an agent may reach only through a
// scoped service.
var agentForbiddenImports = map[string]bool{
	"github.com/lycaon/lycaon/internal/gitcredential":   true,
	"github.com/lycaon/lycaon/internal/credentialstore": true,
}

// agentEffectExemptions name each remaining use by file and what it is.
var agentEffectExemptions = map[string]string{
	"internal/webresearch/credentials.go": "reads the user's configured search provider key for the provider request the host makes",
}

func TestAgentCodeReachesEffectsOnlyThroughTheirDoors(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load AST corpus", err)

	unused := map[string]bool{}
	for rel := range agentEffectExemptions {
		unused[rel] = true
	}
	var offences []string
	for _, gf := range corp.Files() {
		rel := filepath.ToSlash(gf.Rel)
		if gf.IsTest || !hasAnyPrefix(rel, agentReachablePackages) {
			continue
		}
		var found []string
		for _, spec := range gf.AST.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil && agentForbiddenImports[path] {
				found = append(found, "imports "+path)
			}
		}
		names := map[string]map[string]bool{}
		for path, calls := range agentForbiddenCalls {
			if name := importName(gf.AST, path); name != "" {
				names[name] = calls
			}
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := node.X.(*ast.Ident); ok && names[pkg.Name][node.Sel.Name] {
					found = append(found, "uses "+pkg.Name+"."+node.Sel.Name)
				}
			case *ast.CompositeLit:
				if sel, ok := node.Type.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == importName(gf.AST, "net/http") && (sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
						found = append(found, "builds http."+sel.Sel.Name)
					}
				}
			}
			return true
		})
		if len(found) == 0 {
			continue
		}
		if _, exempt := agentEffectExemptions[rel]; exempt {
			delete(unused, rel)
			continue
		}
		for _, f := range found {
			offences = append(offences, rel+" "+f)
		}
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("agent code must launch through confined launch plans, reach the network through outboundhttp "+
			"and use credentials only through scoped services:\n  %s", strings.Join(offences, "\n  "))
	}
	if len(unused) > 0 {
		var stale []string
		for rel := range unused {
			stale = append(stale, rel)
		}
		sort.Strings(stale)
		t.Errorf("these exemptions no longer match anything and should be deleted:\n  %s", strings.Join(stale, "\n  "))
	}
}
