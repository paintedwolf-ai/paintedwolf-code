//go:build darwin || linux

package hostos

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"golang.org/x/sys/unix"
)

var declared = Capabilities{System: true, Identity: true, NumericOwnership: true}

func platformSystem() (System, error) {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return System{}, err
	}
	return System{
		Sysname: unix.ByteSliceToString(name.Sysname[:]), Nodename: unix.ByteSliceToString(name.Nodename[:]),
		Release: unix.ByteSliceToString(name.Release[:]), Version: unix.ByteSliceToString(name.Version[:]),
		Machine: unix.ByteSliceToString(name.Machine[:]),
	}, nil
}

func platformIdentity() (Identity, error) {
	groups, err := os.Getgroups()
	if err != nil {
		return Identity{}, err
	}
	facts := Identity{UID: os.Getuid(), GID: os.Getgid(), Groups: groups, GroupNames: map[int]string{}}
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

func platformCurrentIDs() (int, int) { return os.Getuid(), os.Getgid() }

func platformLookupUser(name string) (int, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return 0, fmt.Errorf("%w: user %s", ErrUnknownAccount, name)
	}
	return numericID(account.Uid)
}

func platformLookupGroup(name string) (int, error) {
	group, err := user.LookupGroup(name)
	if err != nil {
		return 0, fmt.Errorf("%w: group %s", ErrUnknownAccount, name)
	}
	return numericID(group.Gid)
}

func numericID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("account id %q is not numeric", value)
	}
	return id, nil
}

func platformFileOwner(path string) (int, int, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return 0, 0, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	return int(st.Uid), int(st.Gid), nil
}
