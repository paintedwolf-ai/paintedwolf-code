//go:build linux

package launchguard

import (
	"bytes"
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

type intakeMoveInterleaveFacts struct {
	RootInode             uint64 `json:"root_inode"`
	PostMoveMountID       uint64 `json:"post_move_mount_id"`
	PostMoveInode         uint64 `json:"post_move_inode"`
	UnderlyingMountID     uint64 `json:"underlying_mount_id"`
	PreMoveAliasMatched   bool   `json:"pre_move_alias_matched"`
	PostMoveAliasMatched  bool   `json:"post_move_alias_matched"`
	OldPathUnderlying     bool   `json:"old_path_underlying"`
	MovedRowAtTarget      bool   `json:"moved_row_at_target"`
	EpochChanged          bool   `json:"epoch_changed"`
	PollEpochChanged      bool   `json:"poll_epoch_changed"`
	OriginalVerifyRefused bool   `json:"original_verify_refused"`
	MovedVerifySucceeded  bool   `json:"moved_verify_succeeded"`
}

type intakeSnapshotFacts struct {
	SnapshotMountID         uint64                    `json:"snapshot_mount_id"`
	OpenedMountID           uint64                    `json:"opened_mount_id"`
	SnapshotRetired         bool                      `json:"snapshot_retired"`
	IdentityLookupSucceeded bool                      `json:"identity_lookup_succeeded"`
	CaptureRetryable        bool                      `json:"capture_retryable"`
	IntakeRetryable         bool                      `json:"intake_retryable"`
	IntakeTerminal          bool                      `json:"intake_terminal"`
	IntakePending           bool                      `json:"intake_pending"`
	SyscallFailurePreserved bool                      `json:"syscall_failure_preserved"`
	AliasFailureFatal       bool                      `json:"alias_failure_fatal"`
	StructuralFailureFatal  bool                      `json:"structural_failure_fatal"`
	MoveInterleave          intakeMoveInterleaveFacts `json:"move_interleave"`
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
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("isolated owned mount snapshot fixture: %v\n%s\n%s", err, out, stderr.String())
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
	move := facts.MoveInterleave
	if move.RootInode == 0 || move.PostMoveMountID != facts.SnapshotMountID || move.PostMoveInode != move.RootInode ||
		!move.PreMoveAliasMatched || !move.PostMoveAliasMatched || !move.OldPathUnderlying ||
		move.UnderlyingMountID != facts.OpenedMountID || !move.MovedRowAtTarget || !move.EpochChanged ||
		!move.PollEpochChanged || !move.OriginalVerifyRefused || !move.MovedVerifySucceeded {
		t.Fatalf("owned mount move interleave did not prove held-descriptor alias change: %+v", facts)
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
	movedTarget := filepath.Join(root, "owned-volume-moved")
	if err := os.Mkdir(target, 0700); err != nil {
		return facts, err
	}
	if err := os.Mkdir(movedTarget, 0700); err != nil {
		return facts, err
	}
	if err := unix.Mount("none", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=1m"); err != nil {
		return facts, err
	}
	mountedAt := target
	defer func() {
		if mountedAt != "" {
			_ = unix.Unmount(mountedAt, 0)
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
	facts.SyscallFailurePreserved = errors.Is(invalid, unix.EBADF) && errors.As(invalid, &identity) && !intakeLookupUnavailable(invalid) && !errors.Is(invalid, errIntakeEpochChanged)
	originalRoot := root
	movedRoot := root + "-moved"
	if err := os.Rename(root, movedRoot); err != nil {
		_ = unix.Close(descriptor)
		return facts, err
	}
	root, target = movedRoot, filepath.Join(movedRoot, "owned-volume")
	mountedAt = target
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
	mountedAt = target
	facts.SnapshotMountID = uint64(selected.id)
	facts.MoveInterleave, err = observeIntakeMoveInterleave(snapshot, selected, target, movedTarget, &mountedAt)
	if err != nil {
		return facts, err
	}
	if err := unix.Unmount(movedTarget, 0); err != nil {
		return facts, err
	}
	mountedAt = ""
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

func observeIntakeMoveInterleave(snapshot []mountEntry, selected mountEntry, target, movedTarget string, mountedAt *string) (intakeMoveInterleaveFacts, error) {
	var move intakeMoveInterleaveFacts
	infoFD, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return move, err
	}
	defer func() { _ = unix.Close(infoFD) }()
	beforeMove, preMovePollChanged, err := readIntakeMountTopology(infoFD, readMountInfo)
	if err != nil {
		return move, err
	}
	if preMovePollChanged || !sameMountEpoch(snapshot, beforeMove, nil) {
		return move, errors.New("pre-move topology changed before opening source descriptor")
	}
	fd, err := openMountPoint(target)
	if err != nil {
		return move, err
	}
	defer func() { _ = unix.Close(fd) }()
	preID, preInode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return move, err
	}
	preAlias, err := os.Readlink(mountDescriptorPath(fd))
	if err != nil {
		return move, err
	}
	if preID != uint64(selected.id) || preInode == 0 || preAlias != target {
		return move, errors.New("pre-move source descriptor differs from selected mount root")
	}
	move.RootInode = preInode
	move.PreMoveAliasMatched = preAlias == target
	if err := unix.Mount(target, movedTarget, "", unix.MS_MOVE, ""); err != nil {
		return move, err
	}
	*mountedAt = movedTarget
	postMovePollChanged, err := mountInfoEpochChanged(infoFD)
	if err != nil {
		return move, err
	}
	polledFresh, postReadPollChanged, err := readIntakeMountTopology(infoFD, readMountInfo)
	if err != nil {
		return move, err
	}
	postID, postInode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return move, err
	}
	postAlias, err := os.Readlink(mountDescriptorPath(fd))
	if err != nil {
		return move, err
	}
	move.PostMoveMountID, move.PostMoveInode = postID, postInode
	move.PostMoveAliasMatched = postAlias == movedTarget && postAlias != target
	_, fresh, err := readCurrentMountTopology()
	if err != nil {
		return move, err
	}
	underFD, err := openMountPoint(target)
	if err != nil {
		return move, err
	}
	underID, _, underErr := mountDescriptorIdentity(underFD)
	_ = unix.Close(underFD)
	if underErr != nil {
		return move, underErr
	}
	move.UnderlyingMountID = underID
	expectedMoved := selected
	expectedMoved.mountPoint = movedTarget
	var movedEntry mountEntry
	var movedCount int
	oldTargetPresent := false
	for _, entry := range fresh {
		if entry.mountPoint == target {
			oldTargetPresent = true
		}
		if entry.id == selected.id || entry.mountPoint == movedTarget {
			movedEntry = entry
			movedCount++
		}
	}
	move.OldPathUnderlying = underID != 0 && underID != uint64(selected.id) && !oldTargetPresent
	move.MovedRowAtTarget = movedCount == 1 && movedEntry == expectedMoved
	move.EpochChanged = !sameMountEpoch(snapshot, fresh, nil) && sameMountEpoch(polledFresh, fresh, nil)
	move.PollEpochChanged = !preMovePollChanged && postMovePollChanged && !postReadPollChanged
	_, origVerifyErr := verifyIntakeRoot(fd, selected)
	var identity *intakeIdentityError
	move.OriginalVerifyRefused = origVerifyErr != nil && !errors.As(origVerifyErr, &identity) &&
		!intakeLookupUnavailable(origVerifyErr) && !errors.Is(origVerifyErr, errIntakeEpochChanged)
	verifiedMovedInode, movedVerifyErr := verifyIntakeRoot(fd, movedEntry)
	move.MovedVerifySucceeded = movedVerifyErr == nil && verifiedMovedInode == preInode
	return move, nil
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
