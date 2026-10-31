//go:build darwin

package fspath

// HostOperandNames reports the running platform's operand facts.
func HostOperandNames() OperandNames { return DarwinOperandNames() }
