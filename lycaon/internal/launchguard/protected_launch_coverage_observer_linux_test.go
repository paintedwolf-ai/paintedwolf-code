//go:build linux

package launchguard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func captureFixtureOrdinaryBroker() (lifecycleRole, error) {
	bootstrap, err := strconv.Atoi(os.Getenv("PW_FIXTURE_BOOTSTRAP_PID"))
	if err != nil || bootstrap <= 1 {
		return lifecycleRole{}, errors.New("invalid owned fixture bootstrap")
	}
	generation, err := CaptureGeneration()
	if err != nil {
		return lifecycleRole{}, err
	}
	defer generation.Close()
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(bootstrap), "task", strconv.Itoa(bootstrap), "children"))
	if err != nil {
		return lifecycleRole{}, err
	}
	var selected []lifecycleRole
	for _, child := range bytes.Fields(raw) {
		pid, err := strconv.Atoi(string(child))
		if err != nil || pid <= 1 {
			return lifecycleRole{}, errors.New("invalid owned bootstrap child")
		}
		directory, err := unix.Open(filepath.Join("/proc", strconv.Itoa(pid)), unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return lifecycleRole{}, err
		}
		matched, err := matchFixtureMountRole(directory, bootstrap, BrokerAgentArgument, generation.Entry)
		_ = unix.Close(directory)
		if err != nil {
			return lifecycleRole{}, err
		}
		if matched {
			birth, err := lifecycleBirth(pid)
			if err != nil || birth.Parent != bootstrap {
				return lifecycleRole{}, errors.New("ordinary broker birth changed")
			}
			birth.Role = "broker"
			selected = append(selected, birth)
		}
	}
	if len(selected) != 1 {
		return lifecycleRole{}, errors.New("owned bootstrap does not have one selected ordinary broker")
	}
	return selected[0], nil
}

func checkMountIntakePollAuthority(t *testing.T, pid int) {
	t.Helper()
	proc := filepath.Join("/proc", strconv.Itoa(pid))
	descriptors, err := os.ReadDir(filepath.Join(proc, "fd"))
	testutil.FailErr(t, "intake descriptors", err)
	mountinfo := map[string]bool{}
	var epolls []string
	for _, descriptor := range descriptors {
		target, err := os.Readlink(filepath.Join(proc, "fd", descriptor.Name()))
		testutil.FailErr(t, "intake descriptor target", err)
		if strings.HasPrefix(target, proc+"/task/") && filepath.Base(target) == "mountinfo" {
			mountinfo[descriptor.Name()] = true
		}
		if target == "anon_inode:[eventpoll]" {
			epolls = append(epolls, descriptor.Name())
		}
	}
	if len(mountinfo) != 1 {
		t.Fatalf("intake mount notification descriptors = %v, want one", mountinfo)
	}
	for _, epoll := range epolls {
		raw, err := os.ReadFile(filepath.Join(proc, "fdinfo", epoll))
		testutil.FailErr(t, "intake epoll registrations", err)
		for line := range strings.SplitSeq(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "tfd:" && mountinfo[fields[1]] {
				t.Fatalf("mount notification descriptor %s also registered with epoll descriptor %s", fields[1], epoll)
			}
		}
	}
}
