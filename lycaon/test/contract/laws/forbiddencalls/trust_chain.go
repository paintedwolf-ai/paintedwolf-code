package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// trustChainHelperFlags are the helper flags the confined launch pipeline
// passes; the profile travels over the inherited descriptor.
var trustChainHelperFlags = []string{"__confine-exec", "--profile-fd", "--"}

var trustChain = []laws.Rule{{
	// The confined launch pipeline has three inputs. A profile read from the
	// environment, a staged file or an unknown flag would be a fourth.
	Name:    "trust-chain-has-closed-inputs",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "Command"), laws.Func("internal/confine", "runHelper")},
	Check: func(r *laws.Run) {
		for _, name := range []string{"Command", "runHelper"} {
			fn := r.FuncDecl("internal/confine", name)
			f := r.Declaration("internal/confine", name).File
			f.Calls(fn.Body, func(call *ast.CallExpr, pkg, callee string) {
				if pkg != "os" {
					return
				}
				switch callee {
				case "Getenv", "LookupEnv":
					if literalArgMatches(call, mentionsProfile) {
						r.ReportAt(call.Pos(), "%s reads a profile from the environment", name)
					}
				case "Open", "OpenFile", "ReadFile", "Readlink":
					if literalArgMatches(call, mentionsProfile) || literalArgMatches(call, isStagingPath) {
						r.ReportAt(call.Pos(), "%s reads a profile from a file; pass it over the pipe", name)
					}
				}
			})
			laws.StringLits(fn.Body, func(lit *ast.BasicLit, value string) {
				if strings.HasPrefix(value, "--") && !contains(trustChainHelperFlags, value) &&
					(strings.Contains(value, "profile") || strings.Contains(value, "sandbox")) {
					r.ReportAt(lit.Pos(), "%s passes the unknown helper flag %s", name, value)
				}
				if isStagingPath(value) {
					r.ReportAt(lit.Pos(), "%s names a profile staging path", name)
				}
			})
		}
	},
}}

func literalArgMatches(call *ast.CallExpr, match func(string) bool) bool {
	for _, arg := range call.Args {
		if value, ok := laws.StringLit(arg); ok && match(value) {
			return true
		}
	}
	return false
}

func mentionsProfile(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "profile") || strings.Contains(lower, "sandbox") || strings.Contains(lower, "sbpl")
}

func isStagingPath(value string) bool {
	return strings.HasSuffix(value, ".sb") || strings.Contains(value, "lycaon-sbpl-")
}
