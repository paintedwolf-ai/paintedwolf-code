package toolexecution

import (
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
)

// Native reference sinks are explicit immutable operation metadata. The retained
// structured interface's nil-slot whole-argument convention does not make an
// executable name, embedded program or computed destination a disclosure sink.
func validateNativeSecretSlots(contract toolcontract.Contract, args map[string]any) error {
	if !contract.AcceptsSecretReferences() {
		return nil
	}
	if literal, ok := args["resolve_secret_references"].(bool); ok && !literal {
		return nil
	}
	var outside secretcap.ReferenceUse
	if len(contract.SecretReferenceArgs) == 0 {
		outside = secretcap.ReferenceUseInSlots(args, nil)
	} else {
		outside = secretcap.ReferenceUseOutsideSlots(args, contract.SecretReferenceArgs)
	}
	if outside.Complete || outside.Malformed {
		return toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "managed secret reference is outside the operation's declared value sinks", "expected_form": strings.Join(contract.SecretReferenceArgs, ", ")})
	}
	return nil
}
