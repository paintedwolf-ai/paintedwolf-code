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

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const intakeSnapshotFixture = "PW_INTAKE_SNAPSHOT_FIXTURE"

type intakeSnapshotFacts struct {
	SnapshotMountID         uint64 `json:"snapshot_mount_id"`
	OpenedMountID           uint64 `json:"opened_mount_id"`
	SnapshotRetired         bool   `json:"snapshot_retired"`
	IdentityLookupSucceeded bool   `json:"identity_lookup_succeeded"`
	CaptureRetryable        bool   `json:"capture_retryable"`
	IntakeRetryable         bool   `json:"intake_retryable"`
	IntakeTerminal          bool   `json:"intake_terminal"`
	IntakePending           bool   `json:"intake_pending"`
	SyscallFailurePreserved bool   `json:"syscall_failure_preserved"`
	AliasFailureFatal       bool   `json:"alias_failure_fatal"`
	StructuralFailureFatal  bool   `json:"structural_failure_fatal"`
}

func TestIntakeStaleSnapshotFixture(t *testing.T) {
	if os.Getenv(intakeSnapshotFixture) == "" {
		return
	}
	facts, err := staleIntakeSnapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if json.NewEncoder(os.Stdout).Encode(facts) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestIntakeSnapshotAfterOwnedUnmount(t *testing.T) {
	command := exec.Command("sudo", "-n", "env", intakeSnapshotFixture+"=1", os.Args[0], "-test.run=^TestIntakeStaleSnapshotFixture$")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated owned mount snapshot fixture: %v\n%s", err, out)
	}
	var facts intakeSnapshotFacts
	testutil.FailErr(t, "decode actual stale snapshot facts", json.Unmarshal(out, &facts))
	if !facts.SnapshotRetired || !facts.IdentityLookupSucceeded || facts.SnapshotMountID == 0 || facts.OpenedMountID == 0 || facts.SnapshotMountID == facts.OpenedMountID {
		t.Fatalf("fixture did not establish successful underlying-mount lookup: %+v", facts)
	}
	if !facts.CaptureRetryable || !facts.IntakeRetryable || !facts.IntakePending || facts.IntakeTerminal {
		t.Fatalf("verified mount replacement did not select fresh-topology retry: %+v", facts)
	}
	if !facts.SyscallFailurePreserved || !facts.AliasFailureFatal || !facts.StructuralFailureFatal {
		t.Fatalf("retry swallowed a syscall, alias or structural failure: %+v", facts)
	}
	t.Logf("actual intake stale-snapshot facts: %s", out)
}

func staleIntakeSnapshot() (intakeSnapshotFacts, error) {
	var facts intakeSnapshotFacts
	runtime.LockOSThread()
	if os.Geteuid() != 0 {
		return facts, errors.New("owned mount snapshot fixture requires initial root")
	}
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return facts, err
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return facts, err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return facts, err
	}
	root, err := os.MkdirTemp("", "pw-intake-snapshot-")
	if err != nil {
		return facts, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	target := filepath.Join(root, "owned-volume")
	if err := os.Mkdir(target, 0700); err != nil {
		return facts, err
	}
	if err := unix.Mount("none", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=1m"); err != nil {
		return facts, err
	}
	mounted := true
	defer func() {
		if mounted {
			_ = unix.Unmount(target, 0)
		}
	}()
	_, snapshot, err := readCurrentMountTopology()
	if err != nil {
		return facts, err
	}
	var selected mountEntry
	structural := make(map[int]bool, len(snapshot))
	for _, entry := range snapshot {
		if entry.mountPoint == target {
			selected = entry
		} else {
			structural[entry.id] = true
		}
	}
	if selected.id <= 0 {
		return facts, errors.New("owned mount absent from captured snapshot")
	}
	descriptor, _, err := openedIntakeRoot(selected)
	if err != nil {
		return facts, err
	}
	_, invalid := verifyIntakeRoot(-1, selected)
	var identity *intakeIdentityError
	facts.SyscallFailurePreserved = errors.Is(invalid, unix.EBADF) && errors.As(invalid, &identity) && !errors.Is(invalid, errIntakeEpochChanged)
	originalRoot := root
	movedRoot := root + "-moved"
	if err := os.Rename(root, movedRoot); err != nil {
		_ = unix.Close(descriptor)
		return facts, err
	}
	root, target = movedRoot, filepath.Join(movedRoot, "owned-volume")
	aliasID, _, err := mountDescriptorIdentity(descriptor)
	if err != nil || aliasID != uint64(selected.id) {
		_ = unix.Close(descriptor)
		return facts, errors.New("alias control changed its held mount identity")
	}
	_, aliasErr := verifyIntakeRoot(descriptor, selected)
	facts.AliasFailureFatal = aliasErr != nil && !errors.Is(aliasErr, errIntakeEpochChanged)
	_ = unix.Close(descriptor)
	if err := os.Rename(root, originalRoot); err != nil {
		return facts, err
	}
	root, target = originalRoot, selected.mountPoint
	facts.SnapshotMountID = uint64(selected.id)
	if err := unix.Unmount(target, 0); err != nil {
		return facts, err
	}
	mounted = false
	_, current, err := readCurrentMountTopology()
	if err != nil {
		return facts, err
	}
	facts.SnapshotRetired = true
	for _, entry := range current {
		if entry.id == selected.id {
			facts.SnapshotRetired = false
		}
	}
	descriptor, err = openMountPoint(target)
	if err != nil {
		return facts, err
	}
	actual, _, err := mountDescriptorIdentity(descriptor)
	_ = unix.Close(descriptor)
	if err != nil {
		return facts, err
	}
	facts.IdentityLookupSucceeded, facts.OpenedMountID = true, actual
	_, tree, captureErr := captureIntakeLease(selected, indexMountForest(snapshot), snapshot)
	if tree >= 0 {
		_ = unix.Close(tree)
	}
	if captureErr == nil {
		return facts, errors.New("stale snapshot captured the underlying mount")
	}
	facts.CaptureRetryable = errors.Is(captureErr, errIntakeEpochChanged)
	intake := moduleIntake{socket: -1}
	pending, intakeErr := intake.importArrivals(snapshot, structural)
	facts.IntakePending = pending
	_, structuralErr := intake.reconcile(current, map[int]bool{selected.id: true}, map[int]mountEntry{selected.id: selected})
	facts.StructuralFailureFatal = structuralErr != nil && !errors.Is(structuralErr, errIntakeEpochChanged)
	facts.IntakeRetryable = errors.Is(intakeErr, errIntakeEpochChanged)
	facts.IntakeTerminal = intakeErr != nil && !facts.IntakeRetryable
	return facts, nil
}

func TestIntakeLookupRefusalCannotHideIdentitySyscallFailure(t *testing.T) {
	for _, cause := range []error{unix.EACCES, unix.ENOENT, unix.ENOTDIR, unix.ELOOP} {
		if !intakeLookupUnavailable(cause) {
			t.Fatalf("existing path lookup refusal changed: %v", cause)
		}
		identity := &intakeIdentityError{cause}
		if intakeLookupUnavailable(identity) || !errors.Is(identity, cause) || errors.Is(identity, errIntakeEpochChanged) {
			t.Fatalf("identity syscall provenance was discarded or retried: %v", identity)
		}
	}
	for _, failure := range []error{unix.EBADF, unix.EIO, errIntakeEpochChanged, errors.New("structural refusal")} {
		if intakeLookupUnavailable(failure) {
			t.Fatalf("nonlookup failure broadened retry authority: %v", failure)
		}
	}
}
