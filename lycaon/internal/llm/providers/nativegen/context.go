package nativegen

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/inference"
)

func (p *Runtime) admitContext(ctx context.Context, child *rpcProcess) (command, error) {
	minimum := min(4096, p.config.ContextLimit)
	limits := command{Method: "inspect", ContextTokens: p.config.ContextLimit, PrefillChunk: p.config.PrefillChunk}
	low, high := minimum/256, (p.config.ContextLimit-1)/256
	var selected command
	var selectedPlan resourcePlan
	for {
		var resources resourceResult
		if err := child.call(ctx, limits, "resources", inference.Foreground, false, &resources); err != nil {
			return command{}, err
		}
		plan := resources.Plan
		if plan.HostReservationBytes < startupHostBytes {
			return command{}, errors.New("native generation: runtime omitted its startup reservation")
		}
		fits, err := p.contextFits(plan)
		if err != nil {
			return command{}, err
		}
		if fits {
			selected, selectedPlan = limits, plan
			if limits.ContextTokens == p.config.ContextLimit {
				break
			}
			low = limits.ContextTokens/256 + 1
		} else if limits.ContextTokens != p.config.ContextLimit {
			high = limits.ContextTokens/256 - 1
		}
		if low > high || p.config.ContextLimit <= minimum {
			break
		}
		// E4B cache reservations increase monotonically in 256-token blocks.
		limits.ContextTokens = ((low + high) / 2) * 256
	}
	if selected.ContextTokens == 0 {
		return command{}, inference.ErrCapacity
	}
	if err := reserveLoaded(child, &p.config, selectedPlan); err != nil {
		return command{}, err
	}
	selected.Method = "load"
	selected.DeviceBudgetBytes, selected.HostBudgetBytes = selectedPlan.DeviceReservationBytes, selectedPlan.HostReservationBytes
	return selected, nil
}

func (p *Runtime) contextFits(plan resourcePlan) (bool, error) {
	host, device := p.config.HostMemory.Snapshot(), p.config.DeviceMemory.Snapshot()
	if host.Closed || device.Closed {
		return false, inference.ErrClosed
	}
	deviceBytes := plan.DeviceReservationBytes - min(plan.WorkspaceBytes, p.config.SharedWorkspaceBytes)
	hostBytes := plan.HostReservationBytes - startupHostBytes
	if p.config.UnifiedMemory {
		hostBytes += deviceBytes
	}
	return hostBytes <= host.CapacityBytes-host.ReservedBytes && deviceBytes <= device.CapacityBytes-device.ReservedBytes, nil
}
