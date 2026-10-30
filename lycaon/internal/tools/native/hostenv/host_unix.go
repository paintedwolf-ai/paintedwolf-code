//go:build darwin || linux

package hostenv

import (
	"context"
	"os"
	"os/user"
	"strconv"

	"golang.org/x/sys/unix"
)

func (OSHost) System(ctx context.Context) (SystemFacts, error) {
	if err := ctx.Err(); err != nil {
		return SystemFacts{}, err
	}
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return SystemFacts{}, err
	}
	return SystemFacts{
		Sysname: unix.ByteSliceToString(name.Sysname[:]), Nodename: unix.ByteSliceToString(name.Nodename[:]),
		Release: unix.ByteSliceToString(name.Release[:]), Version: unix.ByteSliceToString(name.Version[:]),
		Machine: unix.ByteSliceToString(name.Machine[:]),
	}, nil
}

func (OSHost) Identity(ctx context.Context) (IdentityFacts, error) {
	if err := ctx.Err(); err != nil {
		return IdentityFacts{}, err
	}
	groups, err := os.Getgroups()
	if err != nil {
		return IdentityFacts{}, err
	}
	facts := IdentityFacts{UID: os.Getuid(), GID: os.Getgid(), Groups: groups, GroupNames: map[int]string{}}
	if account, err := user.LookupId(strconv.Itoa(facts.UID)); err == nil {
		facts.User = account.Username
	}
	for _, gid := range append([]int{facts.GID}, groups...) {
		if group, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
			facts.GroupNames[gid] = group.Name
		}
	}
	return facts, nil
}
