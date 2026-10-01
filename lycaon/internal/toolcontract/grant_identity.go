package toolcontract

import "strings"

// GrantIdentityNeutralArgs returns the arguments a reusable approval ignores for
// this tool: result windows, wait and deadline bounds, and output framing. They
// cannot change what an action reads, writes, reaches, or spawns, so moving one
// must not spend another human answer. Compiled from native-tools.yaml.
func GrantIdentityNeutralArgs(tool string) []string {
	args := compiledGrantIdentityNeutralArgs[strings.TrimSpace(tool)]
	if len(args) == 0 {
		return nil
	}
	return append([]string(nil), args...)
}
