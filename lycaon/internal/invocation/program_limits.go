package invocation

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

type programReceiptLimits struct {
	events, bytes, arguments, nodes int64
	terminalEvents, terminalBytes   int64
}

var receiptLimits = sync.OnceValues(func() (programReceiptLimits, error) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return programReceiptLimits{}, err
	}
	limits := programReceiptLimits{}
	for _, limit := range catalog.Language.Limits {
		switch limit.ID {
		case "trace_events":
			limits.events = limit.Maximum
		case "trace_bytes":
			limits.bytes = limit.Maximum
		case "argument_bytes":
			limits.arguments = limit.Maximum
		case "ast_nodes":
			limits.nodes = limit.Maximum
		}
	}
	limits.terminalEvents = catalog.Language.SettlementReserve["per_live_child_events"]
	limits.terminalBytes = catalog.Language.SettlementReserve["per_live_child_bytes"]
	if limits.events < 1 || limits.bytes < 1 || limits.arguments < 1 || limits.nodes < 1 || limits.terminalEvents < 1 || limits.terminalBytes < 1 {
		return limits, fmt.Errorf("compiled language is missing receipt resource bounds")
	}
	return limits, nil
})
