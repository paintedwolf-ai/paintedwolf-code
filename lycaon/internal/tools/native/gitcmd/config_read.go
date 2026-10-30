package gitcmd

import (
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// configOperands: exactly one read action and nothing that sets a value.
func configOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	actions := 0
	for _, key := range []string{"--get", "--get-regexp", "--list", "-l"} {
		actions += len(bound.Options(key))
	}
	if actions != 1 {
		return operandInvalid(spec, spec.Name, "exactly one of --get KEY, --get-regexp PATTERN or --list")
	}
	return noOperands(spec, bound)
}

// configReadArgs consumes the single admitted read action. Fixed complete
// vectors keep user values after the option terminator and cannot become writes.
func configReadArgs(bound commandprogram.BoundArgv) []string {
	if options := bound.Options("--get"); len(options) != 0 {
		return []string{"config", "--local", "--get", "--", options[0].Values[0].Value}
	}
	if options := bound.Options("--get-regexp"); len(options) != 0 {
		return []string{"config", "--local", "--get-regexp", "--", options[0].Values[0].Value}
	}
	return []string{"config", "--local", "--list"}
}
