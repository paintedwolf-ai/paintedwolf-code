package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// NativeOperationSelection freezes host-selected typed definitions independently
// of retained JSON handlers. Definitions carry no execution grants.
type NativeOperationSelection struct {
	offered     commandprogram.OfferedOperations
	known       commandprogram.OfferedOperations
	definitions []toolcontract.OperationDefinition
	denied      map[string]map[string]bool
}

// Definitions returns detached immutable declarations for catalog presentation.
func (s NativeOperationSelection) Definitions() []toolcontract.OperationDefinition {
	return append([]toolcontract.OperationDefinition(nil), s.definitions...)
}

// SelectNativeOperations admits only explicit public native bindings. Registering
// an unrelated hidden JSON handler cannot add it to this catalog.
func (e *Metadata) SelectNativeOperations(ctx context.Context, filter platform.ToolFilter, active []toolcontract.OperationDefinition) (NativeOperationSelection, error) {
	if e == nil || filter.ProfileID == "" {
		return NativeOperationSelection{}, fmt.Errorf("native operation surface requires explicit profile")
	}
	policy, ok := e.policy.(interface {
		CommandOperationAllowed(context.Context, string, string, string) (bool, error)
	})
	if !ok {
		return NativeOperationSelection{}, fmt.Errorf("native operation profile policy required")
	}
	selected := NativeOperationSelection{denied: map[string]map[string]bool{}}
	seen := map[string]bool{}
	for _, definition := range active {
		if err := definition.ValidateNativeCommands(); err != nil {
			return NativeOperationSelection{}, err
		}
		facts := definition.Facts()
		if seen[facts.Identity] {
			return NativeOperationSelection{}, fmt.Errorf("duplicate native operation %q", facts.Identity)
		}
		seen[facts.Identity] = true
		denied := map[string]bool{}
		anyAllowed := false
		for _, command := range facts.Commands {
			allowed, err := policy.CommandOperationAllowed(ctx, filter.ProfileID, facts.Identity, command.Name)
			if err != nil {
				return NativeOperationSelection{}, err
			}
			denied[command.Name] = !allowed
			anyAllowed = anyAllowed || allowed
		}
		selected.denied[facts.Identity] = denied
		if anyAllowed {
			selected.definitions = append(selected.definitions, definition)
			selected.denied[facts.Identity] = denied
		}
	}
	selected.known = commandprogram.Offer(active...)
	selected.offered = commandprogram.Offer(selected.definitions...)
	return selected, nil
}
