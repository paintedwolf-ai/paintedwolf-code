package toolhost

import (
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func registerGitOperationTools(reg *tools.DefaultRegistry, deps buildDeps, boundary *sandbox.Boundary) error {
	if deps.git == nil {
		return nil
	}
	for _, kind := range []string{"checkout", "merge", "stash"} {
		name := "git_" + kind
		if !deps.nativeConfig.HasTool(name) {
			continue
		}
		tool := &native.GitOperationTool{Git: deps.git, Boundary: boundary, Kind: kind}
		if err := reg.Register(name, tool.Run); err != nil {
			return err
		}
	}
	return nil
}
