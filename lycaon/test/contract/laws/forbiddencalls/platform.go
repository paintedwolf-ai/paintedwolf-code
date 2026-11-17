package forbiddencalls

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"path"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// platformAdapters own platform differences under internal/ and report them
// as typed facts, unsupported included.
var platformAdapters = []string{"confine", "exec", "fseffect", "fspath", "hostos", "hostprocess", "lineage", "osprocess"}

// platformConsumers consume adapter facts and never branch on the platform.
var platformConsumers = []string{"commandsurface", "commandprogram", "tools", "toolhost"}

var (
	platformOSNames = []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux",
		"netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos", "unix", "other",
	}
	platformArchNames = []string{
		"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le", "mipsle",
		"ppc64", "ppc64le", "riscv64", "s390x", "wasm",
	}
)

var platform = []laws.Rule{{
	// The command environment's platform code lives in the declared adapters.
	Name:    "command-environment-platform-code-lives-in-adapters",
	Anchors: platformDirAnchors(platformConsumers),
	Check: func(r *laws.Run) {
		for _, finding := range platformFindings(r, platformConsumers) {
			r.Report("%s: platform difference outside the adapters %s", finding, strings.Join(platformAdapters, ", "))
		}
	},
}, {
	// Every declared adapter exists outside the consumers and carries
	// platform code, so declaring a consumer cannot satisfy the rule.
	Name:    "platform-adapter-declaration-is-current",
	Anchors: platformDirAnchors(platformAdapters),
	Check: func(r *laws.Run) {
		for _, adapter := range platformAdapters {
			for _, consumer := range platformConsumers {
				if adapter == consumer || strings.HasPrefix(adapter, consumer+"/") {
					r.Report("%s: declared adapter lies inside consumer %s", adapter, consumer)
				}
			}
			if len(platformFindings(r, []string{adapter})) == 0 {
				r.Report("%s: declared adapter carries no platform code", adapter)
			}
		}
	},
}}

func platformDirAnchors(dirs []string) []laws.Anchor {
	out := make([]laws.Anchor, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, laws.Dir("lycaon/internal/"+dir))
	}
	return out
}

// platformFindings lists platform-suffixed or platform-constrained files and
// runtime.GOOS or GOARCH checks below internal/<root>.
func platformFindings(r *laws.Run, roots []string) []string {
	dirs := make([]string, 0, len(roots))
	for _, root := range roots {
		dirs = append(dirs, "internal/"+root)
	}
	var findings []string
	for _, f := range r.GoFiles(laws.Production, dirs...) {
		if platformFileSuffix(path.Base(f.Rel)) {
			findings = append(findings, f.Rel+": platform-suffixed file")
		}
		if platformBuildConstraint(f.AST) {
			findings = append(findings, f.Rel+": platform build constraint")
		}
		f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
			if pkg == "runtime" && (name == "GOOS" || name == "GOARCH") {
				findings = append(findings, fmt.Sprintf("%s: runtime.%s check", r.Go().Position(sel.Pos()), name))
			}
		})
	}
	return findings
}

func platformFileSuffix(name string) bool {
	parts := strings.Split(strings.TrimSuffix(name, ".go"), "_")
	if len(parts) < 2 {
		return false
	}
	last := parts[len(parts)-1]
	return slices.Contains(platformOSNames, last) || slices.Contains(platformArchNames, last)
}

func platformBuildConstraint(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expr, err := constraint.Parse(comment.Text)
			if err != nil || platformConstraintTag(expr) {
				return true
			}
		}
	}
	return false
}

func platformConstraintTag(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return slices.Contains(platformOSNames, e.Tag) || slices.Contains(platformArchNames, e.Tag)
	case *constraint.NotExpr:
		return platformConstraintTag(e.X)
	case *constraint.AndExpr:
		return platformConstraintTag(e.X) || platformConstraintTag(e.Y)
	case *constraint.OrExpr:
		return platformConstraintTag(e.X) || platformConstraintTag(e.Y)
	}
	return false
}
