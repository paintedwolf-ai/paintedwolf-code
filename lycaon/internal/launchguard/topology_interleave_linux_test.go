//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

type topologyInterleaveFacts struct {
	Reproduced       bool   `json:"reproduced"`
	PrivateNamespace bool   `json:"private_namespace_verified"`
	OldID            uint64 `json:"old_id"`
	NewID            uint64 `json:"new_id"`
	OldInode         uint64 `json:"old_inode"`
	NewInode         uint64 `json:"new_inode"`
	PrefixBytes      int    `json:"prefix_bytes"`
	FurtherRows      int    `json:"further_rows"`
	Replacements     int    `json:"replacements"`
	PollChanged      bool   `json:"poll_changed_during_capture"`
	Predicate        int    `json:"predicate"`
	RefusedID        int    `json:"refused_id"`
	FreshValid       bool   `json:"fresh_capture_valid"`
	Discarded        bool   `json:"changed_capture_discarded_before_admission"`
}

func TestTopologyOwnedInterleaveFixture(t *testing.T) {
	if os.Getenv("PW_TOPOLOGY_OWNED_INTERLEAVE") == "" {
		return
	}
	facts, err := captureOwnedTopologyInterleave()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if json.NewEncoder(os.Stdout).Encode(facts) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestTopologySplitReadAcrossOwnedMountReplacement(t *testing.T) {
	command := exec.Command("sudo", "-n", "env", "PW_TOPOLOGY_OWNED_INTERLEAVE=1", os.Args[0], "-test.run=^TestTopologyOwnedInterleaveFixture$")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("owned split-read mount replacement: %v\n%s", err, out)
	}
	var facts topologyInterleaveFacts
	if err := json.Unmarshal(out, &facts); err != nil {
		t.Fatal(err)
	}
	if !facts.PrivateNamespace || !facts.FreshValid || facts.OldID == 0 || facts.NewID == 0 ||
		facts.PrefixBytes <= 0 || facts.Replacements < 1 || facts.Replacements > 8 || facts.Predicate < -1 || facts.Predicate > int(topologyCycle) {
		t.Fatalf("split-read fixture lost isolation, bounded control or settled validity: %+v", facts)
	}
	if facts.Reproduced && (facts.OldID != facts.NewID || facts.RefusedID != int(facts.OldID) ||
		facts.Predicate != int(topologyDuplicateIdentity) || !facts.PollChanged || !facts.Discarded || facts.FurtherRows == 0) {
		t.Fatalf("duplicate reproduction lost its actual identity/epoch binding: %+v", facts)
	}
	t.Logf("actual owned split-read identity and epoch facts: %s", out)
}

func captureOwnedTopologyInterleave() (facts topologyInterleaveFacts, err error) {
	facts.Predicate = -1
	if err := enterOwnedPrivateTopologyNamespace(); err != nil {
		return facts, err
	}
	facts.PrivateNamespace = true
	root, err := os.MkdirTemp("", "pw-owned-topology-interleave-")
	if err != nil {
		return facts, err
	}
	defer os.RemoveAll(root)
	target := filepath.Join(root, "owned-replacement")
	if err := os.Mkdir(target, 0700); err != nil {
		return facts, err
	}
	mount := func(path string) error {
		return unix.Mount("none", path, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=64k")
	}
	if err := mount(target); err != nil {
		return facts, err
	}
	defer unix.Unmount(target, unix.MNT_DETACH)
	facts.OldID, facts.OldInode, err = ownedTopologyMountIdentity(target)
	if err != nil {
		return facts, err
	}
	var fillers []string
	defer func() {
		for index := len(fillers) - 1; index >= 0; index-- {
			_ = unix.Unmount(fillers[index], unix.MNT_DETACH)
		}
	}()
	for index := range 64 {
		filler := filepath.Join(root, fmt.Sprintf("owned-after-target-%03d", index))
		if err := os.Mkdir(filler, 0700); err != nil {
			return facts, err
		}
		if err := mount(filler); err != nil {
			return facts, err
		}
		fillers = append(fillers, filler)
	}
	info, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return facts, err
	}
	defer unix.Close(info)
	var raw []byte
	entries, changed, captureErr := readIntakeMountTopology(info, func(_ int, _ []byte) ([]byte, error) {
		// Stop only after the exact owned mount's complete structured row. Bytes
		// already returned from this seq-file remain part of this same observation.
		lineStart := 0
		for len(raw) < maxMountTopologyBytes {
			var one [1]byte
			n, err := unix.Read(info, one[:])
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, errors.New("owned selected row absent from stable seq prefix")
			}
			raw = append(raw, one[0])
			if one[0] != '\n' {
				continue
			}
			entry, ok := parseMountInfoLine(string(raw[lineStart : len(raw)-1]))
			if !ok {
				return nil, errors.New("stable seq prefix contains an invalid record")
			}
			lineStart = len(raw)
			if uint64(entry.id) == facts.OldID && entry.mountPoint == target {
				break
			}
		}
		if len(raw) == maxMountTopologyBytes {
			return nil, errors.New("owned seq prefix exceeded topology bound")
		}
		facts.PrefixBytes = len(raw)
		for replacement := range 8 {
			if err := unix.Unmount(target, 0); err != nil {
				return nil, err
			}
			if err := mount(target); err != nil {
				return nil, err
			}
			facts.Replacements = replacement + 1
			facts.NewID, facts.NewInode, err = ownedTopologyMountIdentity(target)
			if err != nil {
				return nil, err
			}
			if facts.NewID == facts.OldID {
				break
			}
		}
		// Finish this FD without seek/reopen; this is an explicitly controlled
		// kernel seq-file experiment, not an alternate production capture path.
		var block [4096]byte
		for len(raw) <= maxMountTopologyBytes {
			n, err := unix.Read(info, block[:])
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if n == 0 {
				break
			}
			raw = append(raw, block[:n]...)
		}
		if len(raw) > maxMountTopologyBytes {
			return nil, errors.New("owned seq tail exceeded topology bound")
		}
		for _, value := range raw[facts.PrefixBytes:] {
			if value == '\n' {
				facts.FurtherRows++
			}
		}
		return raw, nil
	})
	if captureErr != nil {
		return facts, captureErr
	}
	facts.PollChanged = changed
	facts.Discarded = changed && len(entries) == 0
	_, parseErr := parseMountTopology(raw)
	var refusal *mountTopologyRefusal
	if errors.As(parseErr, &refusal) {
		facts.Predicate, facts.RefusedID = int(refusal.predicate), refusal.id
	}
	_, _, freshErr := readCurrentMountTopology()
	facts.FreshValid = freshErr == nil
	if freshErr != nil {
		return facts, freshErr
	}
	facts.Reproduced = facts.OldID == facts.NewID && facts.Predicate == int(topologyDuplicateIdentity) &&
		facts.RefusedID == int(facts.OldID) && facts.PollChanged && facts.Discarded && facts.FurtherRows > 0
	return facts, nil
}

func ownedTopologyMountIdentity(target string) (uint64, uint64, error) {
	fd, err := openMountPoint(target)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(fd)
	return mountDescriptorIdentity(fd)
}
