package scanning

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/guidance"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
)

// RegisterTools registers scan tools in the executor registry.
func (r *Runtime) RegisterTools(reg *tools.DefaultRegistry, rejections *guidance.StaticRejectFormatter, settingsSvc *settings.Service, accounting scantoolapi.InventoryAccounting) error {
	var scannerSettings *settings.SecurityScannersStore
	if settingsSvc != nil {
		scannerSettings = settingsSvc.SecurityScanners
	}
	if err := scantoolapi.RegisterScanTools(reg, r.Coordinator, r.Registry, r.Cadence, rejections, scannerSettings, accounting); err != nil {
		return fmt.Errorf("scan tools: %w", err)
	}
	return nil
}
