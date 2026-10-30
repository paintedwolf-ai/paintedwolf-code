package hitl

import (
	"errors"
	"sync"

	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/pkg/api"
)

type checkpointSocketOwners struct {
	mu     sync.Mutex
	byID   map[string][]*socketref.Lease
	closed bool
}

func approvalSocketBindings(plan *ApprovalPlan) []socketref.Binding {
	if plan == nil {
		return nil
	}
	seen := make(map[string]bool)
	var bindings []socketref.Binding
	add := func(binding socketref.Binding) {
		if binding.Mode() == socketref.KernelPathname && socketref.Platform().Command == socketref.KernelPathname || seen[binding.Key()] {
			return
		}
		seen[binding.Key()] = true
		bindings = append(bindings, binding)
	}
	for _, option := range plan.Options {
		for _, delta := range option.Authority {
			for _, target := range delta.Sockets {
				add(target.Binding)
			}
			if delta.Grant != nil && delta.Grant.Predicate.Category == ApprovalGrantCategorySocketPath {
				add(delta.Grant.Binding)
			}
		}
	}
	return bindings
}

func acquireApprovalSocketOwners(plan *ApprovalPlan) ([]*socketref.Lease, error) {
	var leases []*socketref.Lease
	for _, binding := range approvalSocketBindings(plan) {
		lease, err := binding.Acquire()
		if err != nil {
			closeApprovalSocketOwners(leases)
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, nil
}

func closeApprovalSocketOwners(leases []*socketref.Lease) {
	for _, lease := range leases {
		lease.Close()
	}
}

func (o *checkpointSocketOwners) put(id string, leases []*socketref.Lease) error {
	if len(leases) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return socketref.ErrUnbound
	}
	if o.byID == nil {
		o.byID = make(map[string][]*socketref.Lease)
	}
	o.byID[id] = leases
	return nil
}

func (o *checkpointSocketOwners) release(id string) {
	o.mu.Lock()
	leases := o.byID[id]
	delete(o.byID, id)
	o.mu.Unlock()
	closeApprovalSocketOwners(leases)
}

func (o *checkpointSocketOwners) close() {
	o.mu.Lock()
	o.closed = true
	owners := o.byID
	o.byID = nil
	o.mu.Unlock()
	for _, leases := range owners {
		closeApprovalSocketOwners(leases)
	}
}

// pendingSocketAuthorityLost reads saved identity without reconstructing authority.
func pendingSocketAuthorityLost(row StoredCheckpoint) (bool, error) {
	if row.Kind != api.CheckpointKindToolApproval {
		return false, nil
	}
	raw, exists := row.Payload["approval_plan"]
	if !exists {
		return false, nil
	}
	shape, ok := raw.(map[string]any)
	if !ok {
		return false, errors.New("saved approval plan has an unknown shape")
	}
	plan, err := approvalPlanFromMap(shape)
	if err != nil {
		return false, err
	}
	owners, err := acquireApprovalSocketOwners(plan)
	closeApprovalSocketOwners(owners)
	if errors.Is(err, socketref.ErrUnbound) {
		return true, nil
	}
	return false, err
}
