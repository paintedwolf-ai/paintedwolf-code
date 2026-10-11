//go:build !unix

package toolcommand

func isCurrentOwnershipSpec(_, _ string) bool { return false }
