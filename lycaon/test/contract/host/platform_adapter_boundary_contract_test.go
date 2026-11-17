package contract

import (
	"go/build/constraint"
	"slices"
)

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
