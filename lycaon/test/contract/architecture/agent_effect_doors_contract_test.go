package contract

import (
	"go/ast"
)

// agentForbiddenCalls bypass the launch and network doors: a host launch or
// host Git purpose carries no agent confinement, os/exec starts a process
// outside the launch plan, and the default HTTP client dials without
// destination admission.
var agentForbiddenCalls = map[string]map[string]bool{
	"github.com/lycaon/lycaon/internal/exec":    {"HostLaunch": true},
	"github.com/lycaon/lycaon/internal/gitexec": {"WithHostPurpose": true},
	"os/exec":  {"Command": true, "CommandContext": true},
	"net/http": {"Get": true, "Head": true, "Post": true, "PostForm": true, "NewRequest": true, "NewRequestWithContext": true, "DefaultClient": true, "DefaultTransport": true},
	"net":      {"Dial": true, "DialTimeout": true, "DialTCP": true, "DialUDP": true, "DialUnix": true, "DialIP": true},
}

// agentForbiddenTypes are clients and dialers that reach the network without
// destination admission when an agent package builds its own.
var agentForbiddenTypes = map[string]map[string]bool{
	"net/http": {"Client": true, "Transport": true},
	"net":      {"Dialer": true},
}

// agentEffectUses finds every use of a forbidden call or type in file, as a
// call, a function value or a composite literal, wherever it appears.
func agentEffectUses(file *ast.File) []string {
	calls := map[string]map[string]bool{}
	for path, names := range agentForbiddenCalls {
		if name := importName(file, path); name != "" {
			calls[name] = names
		}
	}
	types := map[string]map[string]bool{}
	for path, names := range agentForbiddenTypes {
		if name := importName(file, path); name != "" {
			types[name] = names
		}
	}
	var found []string
	for path := range agentForbiddenCalls {
		if importName(file, path) == "." {
			found = append(found, "dot-imports "+path)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := node.X.(*ast.Ident); ok && calls[pkg.Name][node.Sel.Name] {
				found = append(found, "uses "+pkg.Name+"."+node.Sel.Name)
			}
		case *ast.CompositeLit:
			if sel, ok := node.Type.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && types[pkg.Name][sel.Sel.Name] {
					found = append(found, "builds "+pkg.Name+"."+sel.Sel.Name)
				}
			}
		}
		return true
	})
	return found
}
