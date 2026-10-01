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
	handlers := map[string]tools.ToolHandler{
		"git_compare":    (&native.GitCompareTool{Git: deps.git}).Run,
		"git_stash_list": (&native.GitStashListTool{Git: deps.git}).Run,
	}
	for _, kind := range []string{"checkout", "merge", "stash"} {
		handlers["git_"+kind] = (&native.GitOperationTool{Git: deps.git, Boundary: boundary, Kind: kind}).Run
	}
	for name, handler := range handlers {
		if deps.nativeConfig.HasTool(name) {
			if err := reg.Register(name, handler); err != nil {
				return err
			}
		}
	}
	return nil
}
