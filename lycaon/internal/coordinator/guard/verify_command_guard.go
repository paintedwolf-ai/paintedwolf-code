package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/oar"
)

const VerifyCommandUndeclaredCode = "VERIFY_COMMAND_UNDECLARED"

// PrepareVerifyCall injects the declared command into a bare verification call.
func PrepareVerifyCall(tool, declared string, args map[string]any) {
	if tool != "verify" {
		return
	}
	if commandsurface.HasCommandInput(args) {
		return
	}
	if declared = strings.TrimSpace(declared); declared != "" && args != nil {
		args["command"] = declared
	}
}

// ObserveVerifyCommandUndeclared publishes bare-verify facts.
func ObserveVerifyCommandUndeclared(tool, declared string, args map[string]any, gc *oar.GuardContext) {
	if gc == nil || tool != "verify" {
		return
	}
	gc.Tool = "verify"
	gc.DeriveToolClassFacts()
	gc.VerifyHasCommand = commandsurface.HasCommandInput(args)
	gc.VerifyDeclared = strings.TrimSpace(declared) != ""
}
