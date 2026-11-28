package command

import (
	"errors"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// GapLaunchable reports whether a native name may run as its installed
// program: a request that names one, found by the launch's own lookup.
func (d *ProgramDispatcher) GapLaunchable(request commandprogram.OperationRequest) bool {
	if len(request.Argv) == 0 || request.Argv[0].Value == "external" {
		return false
	}
	native := d.natives[request.Argv[0].Value]
	// A closed owner or a policy-barred argv refuses wherever it is bound.
	if native.ClosedForms || (native.DisallowGap != nil && native.DisallowGap(request.Argv)) {
		return false
	}
	return (ExternalStages{ToolContext: d.run.tc, Cwd: d.paths.Cwd(), BaseEnvironment: d.run.baseEnv}).Installed(request.Argv[0].Value, request.Environment)
}

// unsupportedForm is the native gap a typed unsupported-form refusal names;
// other refusals (operand errors, policy) are not gaps and never fall back.
func unsupportedForm(spec toolcontract.CommandSpecification, cause error) *toolcontract.NativeGap {
	code, form := "", ""
	var argv *commandprogram.ArgvError
	if errors.As(cause, &argv) {
		code, form = argv.Code, argv.Word
	} else if reject := toolrejection.AsToolReject(cause); reject != nil {
		if reject.NoFallback {
			return nil
		}
		code = reject.Code
		form, _ = reject.Data["word"].(string)
	}
	if code != "COMMAND_OPTION_UNSUPPORTED" && code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		return nil
	}
	return &toolcontract.NativeGap{Command: spec.Name, Code: code, Form: form}
}
