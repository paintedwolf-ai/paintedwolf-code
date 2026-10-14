//go:build !darwin && !linux

package gitexec

import "context"

func defaultAgentSocket(context.Context) string { return "" }
