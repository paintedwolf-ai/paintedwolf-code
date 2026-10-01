package harnessfixture

import (
	_ "embed"
	"encoding/json"
)

//go:embed contract.json
var evaluationContract []byte

// Contract identifies the compiled evaluation boundary independently of fixture revisions.
func Contract() json.RawMessage {
	return append(json.RawMessage(nil), evaluationContract...)
}
