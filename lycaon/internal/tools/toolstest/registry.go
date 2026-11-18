package toolstest

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools"
)

// Registry uses the production registry with optional pre-dispatch failures.
type Registry struct {
	*tools.DefaultRegistry
	failures map[string]error
}

// NewRegistry registers canned read and write handlers.
func NewRegistry() *Registry {
	r := &Registry{DefaultRegistry: tools.NewDefaultRegistry(), failures: make(map[string]error)}
	for _, name := range []string{"read", "write"} {
		err := r.Register(name, func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
			path, _ := args["path"].(string)
			if name == "read" {
				if path == "" {
					path = "unknown"
				}
				return fmt.Sprintf("contents of %s", path), nil
			}
			return fmt.Sprintf("wrote %s", path), nil
		})
		if err != nil {
			panic(err)
		}
	}
	return r
}

// SetFail injects a failure before a handler runs.
func (r *Registry) SetFail(name string, err error) { r.failures[name] = err }

func (r *Registry) Run(ctx context.Context, name string, args map[string]any, tc tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err, ok := r.failures[name]; ok {
		return "", err
	}
	return r.DefaultRegistry.Run(ctx, name, args, tc)
}
