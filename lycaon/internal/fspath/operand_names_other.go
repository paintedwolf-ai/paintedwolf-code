//go:build !darwin && !windows

package fspath

// HostOperandNames reports the running platform's operand facts; Linux and
// other POSIX platforms keep operands as exact bytes.
func HostOperandNames() OperandNames { return LinuxOperandNames() }
