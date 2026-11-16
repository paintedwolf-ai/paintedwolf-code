package forbiddencalls

import (
	"slices"

	"go/ast"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// agentAdapterPackages reach files on an agent's behalf through
// ProgramPaths, projectpaths and fseffect, which hold the admitted object.
var agentAdapterPackages = []string{"internal/tools/native", "internal/httpaction", "internal/commandprogram"}

// agentReachablePackages run on an agent's behalf.
var agentReachablePackages = append([]string{"internal/toolexecution", "internal/webresearch"}, agentAdapterPackages...)

// agentForbiddenRefs bypass the launch and network doors: a host launch or
// host Git purpose carries no agent confinement, os/exec starts a process
// outside the launch plan, and the default HTTP client dials without
// destination admission.
var agentForbiddenRefs = map[string][]string{
	"github.com/lycaon/lycaon/internal/exec":    {"HostLaunch"},
	"github.com/lycaon/lycaon/internal/gitexec": {"WithHostPurpose"},
	"os/exec":  {"Command", "CommandContext"},
	"net/http": {"Get", "Head", "Post", "PostForm", "NewRequest", "NewRequestWithContext", "DefaultClient", "DefaultTransport"},
	"net":      {"Dial", "DialTimeout", "DialTCP", "DialUDP", "DialUnix", "DialIP"},
}

// agentForbiddenBuilds reach the network without destination admission
// when an agent package builds its own.
var agentForbiddenBuilds = map[string][]string{
	"net/http": {"Client", "Transport"},
	"net":      {"Dialer"},
}

// rawFilesystemCalls reopen or change a pathname outside the door.
var rawFilesystemCalls = []string{
	"Open", "OpenFile", "ReadFile", "WriteFile", "Create", "CreateTemp", "Remove", "RemoveAll", "Rename",
	"Mkdir", "MkdirAll", "MkdirTemp", "Symlink", "Link", "Chmod", "Chown", "Lchown", "Chtimes", "Truncate", "ReadDir",
}

var agentDoors = []laws.Rule{{
	// Agent code launches through confined launch plans, reaches the network
	// through outboundhttp and holds credentials only through scoped services.
	Name: "agent-effects-through-their-doors",
	Anchors: []laws.Anchor{
		laws.Func("internal/exec", "HostLaunch"),
		laws.Func("internal/gitexec", "WithHostPurpose"),
		laws.Package("internal/outboundhttp"),
	},
	Exempt: []laws.Exemption{{
		Key:    "internal/webresearch/credentials.go",
		Reason: "reads the user's configured search provider key for the provider request the host makes",
	}},
	Check: func(r *laws.Run) {
		credentials := []string{"github.com/lycaon/lycaon/internal/gitcredential", "github.com/lycaon/lycaon/internal/credentialstore"}
		for _, f := range r.GoFiles(laws.Production, agentReachablePackages...) {
			var found []string
			for _, p := range credentials {
				if f.Imports(p) {
					found = append(found, "imports "+p)
				}
			}
			for p := range agentForbiddenRefs {
				if f.DotImports(p) {
					found = append(found, "dot-imports "+p)
				}
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if slices.Contains(agentForbiddenRefs[pkg], name) {
					found = append(found, "uses "+laws.SelectorName(sel))
				}
			})
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok {
					if pkg, name, ok := f.Qualified(lit.Type); ok && slices.Contains(agentForbiddenBuilds[pkg], name) {
						found = append(found, "builds "+laws.SelectorName(lit.Type))
					}
				}
				return true
			})
			if len(found) == 0 || r.Exempt(f.Rel) {
				continue
			}
			for _, use := range found {
				r.Report("%s %s: launch through confined launch plans, reach the network through outboundhttp, "+
					"use credentials only through scoped services", f.Rel, use)
			}
		}
	},
}, {
	// Agent adapters read and change files through ProgramPaths, projectpaths
	// or fseffect instead of reopening a pathname.
	Name:    "agent-adapters-reach-files-through-the-door",
	Anchors: []laws.Anchor{laws.Package("internal/fseffect"), laws.Package("internal/tools/projectpaths")},
	Exempt: []laws.Exemption{
		{Key: "internal/tools/native/command/command_not_found.go:resolvableNeighbours", Reason: "lists PATH directories for command-not-found suggestions; names only, never project content"},
		{Key: "internal/tools/native/gitcmd/remove.go:pruneEmptyParents", Reason: "checks emptiness of directories inside the bound repository before the delete door removes them"},
		{Key: "internal/tools/native/sourceview/rejects.go:PathNotFound", Reason: "lists the parent of a resolved missing path for spelling suggestions"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, agentAdapterPackages...) {
			if f.DotImports("os") {
				r.Report("%s dot-imports os", f.Rel)
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "os" && slices.Contains(rawFilesystemCalls, name) && !r.Exempt(f.Site(sel.Pos())) {
					r.Report("%s uses os.%s: go through ProgramPaths, projectpaths or fseffect", f.Site(sel.Pos()), name)
				}
			})
		}
	},
}}
