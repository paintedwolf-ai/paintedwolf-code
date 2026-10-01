//go:build unix

package tools

import (
	"os"
	"os/user"
	"strconv"
)

func isCurrentOwnershipSpec(owner, group string) bool {
	return resolvesToUID(owner, os.Getuid()) && resolvesToGID(group, os.Getgid())
}

func resolvesToUID(spec string, want int) bool {
	if id, err := strconv.Atoi(spec); err == nil {
		return id == want
	}
	account, err := user.Lookup(spec)
	return err == nil && account.Uid == strconv.Itoa(want)
}

func resolvesToGID(spec string, want int) bool {
	if id, err := strconv.Atoi(spec); err == nil {
		return id == want
	}
	group, err := user.LookupGroup(spec)
	return err == nil && group.Gid == strconv.Itoa(want)
}
