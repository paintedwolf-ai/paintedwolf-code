package surface

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func applyAskCatalogCeiling(plan toolsurface.Plan) toolsurface.Plan {
	for _, name := range plan.AddressableNames() {
		contract, known := toolcontract.Lookup(name)
		if !known || !contract.AskAllowed {
			plan = plan.Without(name)
		}
	}
	return plan
}
