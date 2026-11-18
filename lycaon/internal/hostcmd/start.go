package hostcmd

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/exec"
)

// PrepareStart checks the first group before publishing its inputs. Executable
// handles are captured afterward because preparation can replace a script.
func (r Request) PrepareStart(ctx context.Context) (SourceIdentity, error) {
	if r.BeforeStart == nil {
		return SourceIdentity{}, nil
	}
	if err := ctx.Err(); err != nil {
		return SourceIdentity{}, err
	}
	if check := r.Launch.CurrentAuthority; check != nil {
		if err := check(ctx); err != nil {
			return SourceIdentity{}, err
		}
	}
	groups := exec.GroupStages(r.Stages)
	if len(groups) == 0 {
		return SourceIdentity{}, fmt.Errorf("pipeline requires at least one stage")
	}
	first := groups[0]
	opts := exec.ExecOpts{Dir: r.ProjectDir, Launch: r.Launch, PathExtra: r.PathExtra, ServiceEnv: r.ServiceEnv}
	for _, stage := range r.Stages[first.Start:first.End] {
		opts.InlineEnv = exec.MergeInlineEnv(r.InlineEnv, stage.Env)
		if _, err := exec.LaunchLookup(stage.Name, opts); err != nil {
			return SourceIdentity{}, err
		}
	}
	return r.BeforeStart(ctx)
}
