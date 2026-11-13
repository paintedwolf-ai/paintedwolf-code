package evidence

import (
	"sync"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

var nativeOperations = sync.OnceValues(func() (map[string]bool, error) {
	cfg, err := nativemanifest.Load()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, name := range cfg.AllOperations() {
		out[name] = true
	}
	return out, nil
})

// MintedKinds maps every evidence kind to whether the reachable tools mint it,
// directly or through a native operation command reaches.
func (b *Binding) MintedKinds(reachable map[string]bool) (map[string]bool, error) {
	operations, err := nativeOperations()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for kind := range b.KindShapeMap() {
		out[kind] = false
	}
	for _, tool := range b.DeclaredToolNames() {
		if !reachable[tool] && !(operations[tool] && toolcontract.CommandLanguageReachable(tool, reachable)) {
			continue
		}
		for _, kind := range b.ProducibleKinds(tool) {
			out[kind] = true
		}
	}
	return out, nil
}
