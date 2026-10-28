//go:build linux

package launchguard

import (
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

// threadIdentity is one OS thread's own view of its credentials.
type threadIdentity struct {
	uids   [3]int
	gids   [3]int
	groups []int
}

func readThreadIdentity() threadIdentity {
	var id threadIdentity
	id.uids[0], id.uids[1], id.uids[2] = unix.Getresuid()
	id.gids[0], id.gids[1], id.gids[2] = unix.Getresgid()
	id.groups, _ = unix.Getgroups()
	return id
}

// The privilege drop is irreversible, so it runs as root in a child process.
// The kernel keeps credentials per thread, so threads pinned before the drop
// must each report the caller's complete identity afterwards, and the kernel's
// own record of every task must agree.
func TestDropPrivilegesReachesEveryThread(t *testing.T) {
	const childEnv = "PW_LAUNCH_PRIVILEGE_DROP_TEST"
	if os.Getenv(childEnv) == "" {
		if out, err := exec.Command("sudo", "-n", "true").CombinedOutput(); err != nil {
			t.Fatalf("the privilege drop contract needs passwordless sudo: %v %s", err, out)
		}
		groups, err := unix.Getgroups()
		testutil.FailErr(t, "caller groups", err)
		identity := []string{strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())}
		for _, group := range groups {
			identity = append(identity, strconv.Itoa(group))
		}
		out, err := exec.Command("sudo", "-n", "env", childEnv+"="+strings.Join(identity, ","), os.Args[0],
			"-test.run=^TestDropPrivilegesReachesEveryThread$", "-test.v").CombinedOutput()
		if err != nil {
			t.Fatalf("privilege drop child: %v\n%s", err, out)
		}
		return
	}
	var ids []uint32
	for _, field := range strings.Split(os.Getenv(childEnv), ",") {
		id, err := strconv.ParseUint(field, 10, 32)
		testutil.FailErr(t, "caller identity", err)
		ids = append(ids, uint32(id))
	}
	caller := Identity{UID: ids[0], GID: ids[1], Groups: ids[2:]}
	if before := readThreadIdentity(); before.uids != [3]int{0, 0, 0} || caller.UID == 0 {
		t.Fatalf("the child must start as root and drop to a login user: %+v, caller %+v", before, caller)
	}
	wantGroups := make([]int, len(caller.Groups))
	for i, group := range caller.Groups {
		wantGroups[i] = int(group)
	}
	const pinned = 8
	ready := make(chan struct{}, pinned)
	proceed := make(chan struct{})
	reports := make(chan threadIdentity, pinned)
	for range pinned {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ready <- struct{}{}
			<-proceed
			reports <- readThreadIdentity()
		}()
	}
	for range pinned {
		<-ready
	}
	testutil.FailErr(t, "drop privileges", dropPrivileges(caller))
	close(proceed)
	want := threadIdentity{uids: [3]int{int(caller.UID), int(caller.UID), int(caller.UID)}, gids: [3]int{int(caller.GID), int(caller.GID), int(caller.GID)}, groups: wantGroups}
	for i := 0; i <= pinned; i++ {
		got := readThreadIdentity()
		if i < pinned {
			got = <-reports
		}
		if got.uids != want.uids || got.gids != want.gids || !slices.Equal(got.groups, want.groups) {
			t.Fatalf("thread %d reports %+v after the drop, want %+v on every thread", i, got, want)
		}
	}
	checked, err := verifyProcessThreadsStatus(os.Getpid(), int(caller.UID), int(caller.GID), wantGroups, "0")
	testutil.FailErr(t, "kernel task credentials", err)
	if checked <= pinned {
		t.Fatalf("checked %d tasks, want more than the %d pinned threads", checked, pinned)
	}
}
