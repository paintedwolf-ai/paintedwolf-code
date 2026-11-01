package nativegen

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/inference"
)

func (p *Runtime) admitContext(ctx context.Context, child *rpcProcess) (command, error) {
	minimum := min(4096, p.config.ContextLimit)
	limits := command{Method: "inspect", ContextTokens: p.config.ContextLimit, PrefillChunk: p.config.PrefillChunk}
	for {
		var resources resourceResult
		if err := child.call(ctx, limits, "resources", inference.Foreground, false, &resources); err != nil {
			return command{}, err
		}
		plan := resources.Plan
		if plan.HostReservationBytes < startupHostBytes {
			return command{}, errors.New("native generation: runtime omitted its startup reservation")
		}
		err := reserveLoaded(child, &p.config, plan)
		if err == nil {
			limits.Method = "load"
			limits.DeviceBudgetBytes, limits.HostBudgetBytes = plan.DeviceReservationBytes, plan.HostReservationBytes
			return limits, nil
		}
		if !errors.Is(err, inference.ErrCapacity) || limits.ContextTokens == minimum {
			return command{}, err
		}
		limits.ContextTokens = max(minimum, limits.ContextTokens/2)
	}
}
