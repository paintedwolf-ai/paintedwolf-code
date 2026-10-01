//go:build !unix

package tools

func isCurrentOwnershipSpec(_, _ string) bool { return false }
