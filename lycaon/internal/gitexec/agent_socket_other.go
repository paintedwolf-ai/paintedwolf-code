//go:build !darwin

package gitexec

import "context"

func defaultAgentSocket(context.Context) string { return "" }
