//go:build windows

package fspath

// HostOperandNames reports the running platform's operand facts.
func HostOperandNames() OperandNames { return WindowsOperandNames() }
