package commandsurface

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
)

// CommandIdentity preserves executable arguments and connectors while ignoring quote syntax.
func CommandIdentity(line string) string {
	stages, err := exec.StagesFromCommandLine(strings.TrimSpace(line))
	if err != nil {
		return ""
	}
	raw, err := json.Marshal(stages)
	if err != nil {
		return ""
	}
	return string(raw)
}

// SameCommandLine compares executable arguments and connectors; invalid commands do not match.
func SameCommandLine(left, right string) bool {
	identity := CommandIdentity(left)
	return identity != "" && identity == CommandIdentity(right)
}

// CheckKey groups settled runs, including structured pipelines that are not command-line inputs.
func CheckKey(command, cwd string) string {
	identity := CommandIdentity(command)
	if identity == "" {
		identity = "rendered:" + command
	}
	return identity + "\x00" + cwd
}
