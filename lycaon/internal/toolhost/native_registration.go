package toolhost

import (
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// registerCommandOperations publishes the contracts command programs bind for
// work that has no model-callable tool.
func registerCommandOperations(reg *tools.DefaultRegistry, config nativemanifest.Config) error {
	names, err := toolcontract.CommandOperations()
	if err != nil {
		return err
	}
	for _, name := range names {
		if !config.HasOperation(name) {
			continue
		}
		if err := reg.RegisterOperation(name); err != nil {
			return err
		}
	}
	return nil
}
