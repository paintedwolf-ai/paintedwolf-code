//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

type topologyPollerFacts struct {
	PrivateNamespace bool `json:"private_namespace_verified"`
	GoMemberships    int  `json:"go_reader_epoll_memberships_after_fd"`
	RawMemberships   int  `json:"raw_reader_epoll_memberships"`
	EventPolls       int  `json:"owned_eventpoll_descriptors"`
	Attempts         int  `json:"attempts"`
	Consumed         bool `json:"hidden_poll_consumption_reproduced"`
	GoChanged        bool `json:"go_reader_post_epoch_changed"`
	RawChanged       bool `json:"raw_reader_post_epoch_changed"`
	FreshValid       bool `json:"fresh_capture_valid"`
}

func TestTopologyOwnedPollerFixture(t *testing.T) {
	if os.Getenv("PW_TOPOLOGY_OWNED_POLLER") == "" {
		return
	}
	facts, err := captureOwnedTopologyPoller()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if json.NewEncoder(os.Stdout).Encode(facts) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestTopologyOwnedReaderHasExclusiveEpochNotifications(t *testing.T) {
	command := exec.Command("sudo", "-n", "env", "PW_TOPOLOGY_OWNED_POLLER=1", os.Args[0], "-test.run=^TestTopologyOwnedPollerFixture$")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("owned reader poll experiment: %v\n%s\n%s", err, out, stderr.String())
	}
	var facts topologyPollerFacts
	if err := json.Unmarshal(out, &facts); err != nil {
		testutil.FailErr(t, "decode exclusive mount notification facts", err)
	}
	if !facts.PrivateNamespace || !facts.FreshValid || facts.RawMemberships != 0 || facts.Attempts < 1 || facts.Attempts > 8 || facts.GoMemberships < 0 || facts.EventPolls < facts.GoMemberships {
		t.Fatalf("poll experiment lost bounded isolation/descriptor facts: %+v", facts)
	}
	if facts.Consumed && (facts.GoMemberships == 0 || facts.GoChanged || !facts.RawChanged) {
		t.Fatalf("hidden consumer reproduction lacks actual registrations/epoch comparison: %+v", facts)
	}
	t.Logf("actual owned reader poll facts: %s", out)
}

func captureOwnedTopologyPoller() (facts topologyPollerFacts, err error) {
	if err := enterOwnedPrivateTopologyNamespace(); err != nil {
		return facts, err
	}
	facts.PrivateNamespace = true
	root, err := os.MkdirTemp("", "pw-owned-topology-poller-")
	if err != nil {
		return facts, err
	}
	defer os.RemoveAll(root)
	target := filepath.Join(root, "owned-replacement")
	if err := os.Mkdir(target, 0700); err != nil {
		return facts, err
	}
	mount := func() error {
		return unix.Mount("none", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=64k")
	}
	if err := mount(); err != nil {
		return facts, err
	}
	defer unix.Unmount(target, unix.MNT_DETACH)
	goReader, err := os.Open("/proc/thread-self/mountinfo")
	if err != nil {
		return facts, err
	}
	defer goReader.Close()
	goFD := int(goReader.Fd()) // Match the monitor's os.Open followed by Fd.
	rawFD, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return facts, err
	}
	defer unix.Close(rawFD)
	facts.EventPolls, facts.GoMemberships, facts.RawMemberships, err = ownedReaderEpollMemberships(goFD, rawFD)
	if err != nil {
		return facts, err
	}
	for attempt := range 8 {
		if _, err := mountInfoEpochChanged(goFD); err != nil {
			return facts, err
		}
		if _, err := mountInfoEpochChanged(rawFD); err != nil {
			return facts, err
		}
		if err := unix.Unmount(target, 0); err != nil {
			return facts, err
		}
		if err := mount(); err != nil {
			return facts, err
		}
		// Yield through the real runtime poller; no concurrent user-space poll
		// is issued for either selected descriptor during this interval.
		time.Sleep(5 * time.Millisecond)
		facts.Attempts = attempt + 1
		facts.GoChanged, err = mountInfoEpochChanged(goFD)
		if err != nil {
			return facts, err
		}
		facts.RawChanged, err = mountInfoEpochChanged(rawFD)
		if err != nil {
			return facts, err
		}
		facts.Consumed = facts.GoMemberships > 0 && !facts.GoChanged && facts.RawChanged
		if facts.Consumed {
			break
		}
	}
	_, _, err = readCurrentMountTopology()
	facts.FreshValid = err == nil
	return facts, err
}

// Read only this fixture process's eventpoll descriptor metadata, retaining
// membership counts for the two known readers rather than unrelated FD facts.
func ownedReaderEpollMemberships(goFD, rawFD int) (polls, goMembers, rawMembers int, err error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, 0, 0, err
	}
	if len(entries) > 128 {
		return 0, 0, 0, errors.New("owned descriptor inventory exceeds bound")
	}
	for _, entry := range entries {
		name := filepath.Join("/proc/self/fd", entry.Name())
		link, linkErr := os.Readlink(name)
		if errors.Is(linkErr, os.ErrNotExist) {
			continue
		} // ReadDir's own closed FD.
		if linkErr != nil {
			return 0, 0, 0, linkErr
		}
		if link != "anon_inode:[eventpoll]" {
			continue
		}
		polls++
		if polls > 8 {
			return 0, 0, 0, errors.New("owned eventpoll inventory exceeds bound")
		}
		file, openErr := os.Open(filepath.Join("/proc/self/fdinfo", entry.Name()))
		if openErr != nil {
			return 0, 0, 0, openErr
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return 0, 0, 0, errors.Join(readErr, closeErr)
		}
		if len(raw) > 64<<10 {
			return 0, 0, 0, errors.New("owned eventpoll metadata exceeds bound")
		}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			fields := strings.Fields(string(line))
			if len(fields) == 0 || fields[0] != "tfd:" {
				continue
			}
			if len(fields) < 3 || fields[2] != "events:" {
				return 0, 0, 0, errors.New("owned eventpoll membership has invalid shape")
			}
			target, err := strconv.Atoi(fields[1])
			if err != nil || target < 0 {
				return 0, 0, 0, errors.New("owned eventpoll target is invalid")
			}
			if target == goFD {
				goMembers++
			}
			if target == rawFD {
				rawMembers++
			}
		}
	}
	return polls, goMembers, rawMembers, nil
}
