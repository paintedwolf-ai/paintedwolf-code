//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

type topologyChurnFacts struct {
	Predicate   int  `json:"predicate"`
	Bytes       int  `json:"bytes"`
	Rows        int  `json:"rows"`
	ID          int  `json:"id"`
	Parent      int  `json:"parent"`
	Roots       int  `json:"roots"`
	PollChanged bool `json:"poll_changed_during_capture"`
	FreshValid  bool `json:"fresh_capture_valid_after_worker"`
	Iterations  int  `json:"iterations"`
}

func TestTopologyOwnedChurnFixture(t *testing.T) {
	if os.Getenv("PW_TOPOLOGY_OWNED_CHURN") == "" {
		return
	}
	facts, err := captureOwnedTopologyChurn()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(facts); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestTopologyCaptureDuringOwnedPrivateMountChurn(t *testing.T) {
	command := exec.Command("sudo", "-n", "env", "PW_TOPOLOGY_OWNED_CHURN=1", os.Args[0], "-test.run=^TestTopologyOwnedChurnFixture$")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("owned private mount churn observation: %v\n%s", err, out)
	}
	var facts topologyChurnFacts
	if err := json.Unmarshal(out, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Predicate < 0 || facts.Predicate > int(topologyCycle) || !facts.PollChanged || !facts.FreshValid || facts.Bytes <= 0 {
		t.Fatalf("fixture did not establish an actual changed seq-file capture and valid settled topology: %+v", facts)
	}
	t.Logf("actual owned mount churn scalar facts: %s", out)
}

func captureOwnedTopologyChurn() (facts topologyChurnFacts, err error) {
	facts.Predicate = -1
	runtime.LockOSThread()
	if os.Geteuid() != 0 {
		return facts, errors.New("owned topology churn requires initial root")
	}
	if err := unix.Unshare(unix.CLONE_FS | unix.CLONE_NEWNS); err != nil {
		return facts, err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return facts, err
	}
	root, err := os.MkdirTemp("", "pw-owned-topology-churn-")
	if err != nil {
		return facts, err
	}
	defer os.RemoveAll(root)
	// Ensure the real bounded reader requires multiple seq-file reads.
	for index := range 64 {
		target := filepath.Join(root, fmt.Sprintf("owned-filler-%03d", index))
		if err := os.Mkdir(target, 0700); err != nil {
			return facts, err
		}
		if err := unix.Mount("none", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=64k"); err != nil {
			return facts, err
		}
		defer unix.Unmount(target, unix.MNT_DETACH)
	}
	target := filepath.Join(root, "owned-churn")
	if err := os.Mkdir(target, 0700); err != nil {
		return facts, err
	}
	namespace, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return facts, err
	}
	defer namespace.Close()
	info, err := os.Open("/proc/thread-self/mountinfo")
	if err != nil {
		return facts, err
	}
	defer info.Close()
	done := make(chan struct{})
	start := make(chan struct{})
	var workerErr error
	go func() {
		workerErr = mutateOwnedTopology(namespace, target, start)
		close(done)
	}()
	close(start)
	// Always settle the owned worker before releasing namespace descriptors or
	// removing its directory, including a reader failure.
	defer func() { <-done; err = errors.Join(err, workerErr) }()
	for iteration := range 1024 {
		before := []unix.PollFd{{Fd: int32(info.Fd()), Events: unix.POLLPRI | unix.POLLERR}}
		if _, err := unix.Poll(before, 0); err != nil {
			return facts, err
		}
		raw, err := readMountInfo(int(info.Fd()), nil)
		if err != nil {
			return facts, err
		}
		after := []unix.PollFd{{Fd: int32(info.Fd()), Events: unix.POLLPRI | unix.POLLERR}}
		if _, err := unix.Poll(after, 0); err != nil {
			return facts, err
		}
		_, parseErr := parseMountTopology(raw)
		var refusal *mountTopologyRefusal
		if errors.As(parseErr, &refusal) {
			facts = topologyChurnFacts{int(refusal.predicate), refusal.bytes, refusal.rows, refusal.id, refusal.parent, refusal.roots,
				after[0].Revents&(unix.POLLPRI|unix.POLLERR) != 0, false, iteration + 1}
			break
		}
	}
	// Join once here so the fresh snapshot has no remaining fixture mutator.
	<-done
	if workerErr != nil {
		return facts, workerErr
	}
	_, _, freshErr := readCurrentMountTopology()
	facts.FreshValid = freshErr == nil
	if freshErr != nil {
		return facts, freshErr
	}
	if facts.Predicate < 0 {
		return facts, errors.New("bounded owned churn produced no refused capture")
	}
	return facts, nil
}

func mutateOwnedTopology(namespace *os.File, target string, start <-chan struct{}) error {
	runtime.LockOSThread()
	// Only this worker joins the creator's captured private namespace, with an
	// independently detached filesystem context rather than another Go thread's.
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return err
	}
	if err := unix.Setns(int(namespace.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	<-start
	defer unix.Unmount(target, unix.MNT_DETACH)
	for range 512 {
		if err := unix.Mount("none", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=64k"); err != nil {
			return err
		}
		if err := unix.Unmount(target, 0); err != nil {
			return err
		}
	}
	return nil
}
