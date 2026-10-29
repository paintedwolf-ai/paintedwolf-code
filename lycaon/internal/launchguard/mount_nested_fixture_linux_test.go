//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The namespace root was proved private by enterOwnedPrivateTopologyNamespace.
// Only this exclusive bind becomes a propagation source for the real slave intake.
func shareOwnedNestedRoot(root string) error {
	before, err := os.Stat(root)
	if err != nil {
		return err
	}
	if err := unix.Mount(root, root, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	fd, err := openMountPoint(root)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	id, inode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return err
	}
	after, err := os.Stat(root)
	if err != nil || !os.SameFile(before, after) {
		return errors.New("owned bind changed its selected root object")
	}
	_, rows, err := readCurrentMountTopology()
	if err != nil {
		return err
	}
	selected := false
	for _, row := range rows {
		if uint64(row.id) == id && row.mountPoint == root && row.shared == 0 && row.master == 0 {
			selected = true
		}
		if row.shared != 0 || row.master != 0 || row.propagateFrom != 0 {
			return errors.New("unexpected propagation before selecting owned bind")
		}
	}
	if !selected {
		return errors.New("fresh owned bind lacks exact private mount identity")
	}
	if err := unix.Mount("", root, "", unix.MS_SHARED, ""); err != nil {
		return err
	}
	_, rows, err = readCurrentMountTopology()
	if err != nil {
		return err
	}
	selected = false
	for _, row := range rows {
		if uint64(row.id) == id && row.mountPoint == root && row.shared != 0 && row.master == 0 {
			selected = true
			fmt.Printf("PW_NESTED_SOURCE01 {\"mount_id\":%d,\"parent_id\":%d,\"root_inode\":%d,\"shared_peer\":%d,\"device\":%q}\n", id, row.parent, inode, row.shared, row.device)
		} else if row.shared != 0 || row.master != 0 || row.propagateFrom != 0 {
			return errors.New("shared propagation escaped exact owned bind")
		}
	}
	if !selected {
		return errors.New("owned bind did not acquire its exact shared peer identity")
	}
	return nil
}

func prepareNestedMountFixture(root string, uid, gid int) (hidden int, err error) {
	hidden = -1
	defer func() {
		if err != nil && hidden >= 0 {
			_ = unix.Close(hidden)
			hidden = -1
		}
	}()
	for _, name := range []string{"home", "target", "source", "hidden", "real"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			return hidden, err
		}
	}
	if err := os.Chown(filepath.Join(root, "home"), uid, gid); err != nil {
		return hidden, err
	}
	if err := os.WriteFile(filepath.Join(root, "target", "underlying"), []byte("underlying"), 0644); err != nil {
		return hidden, err
	}
	if err := mountNestedPayload(filepath.Join(root, "source"), "root"); err != nil {
		return hidden, err
	}
	for _, name := range []string{"child", "added"} {
		if err := os.Mkdir(filepath.Join(root, "source", name), 0755); err != nil {
			return hidden, err
		}
	}
	if err := mountNestedPayload(filepath.Join(root, "source", "child"), "child"); err != nil {
		return hidden, err
	}
	// The initial cover must never reveal its hidden older child in the app view.
	if err := mountNestedPayload(filepath.Join(root, "hidden"), "hidden-root"); err != nil {
		return hidden, err
	}
	if err := os.Mkdir(filepath.Join(root, "hidden", "buried"), 0755); err != nil {
		return hidden, err
	}
	if err := mountNestedPayload(filepath.Join(root, "hidden", "buried"), "buried"); err != nil {
		return hidden, err
	}
	if err := os.Mkdir(filepath.Join(root, "hidden", "late"), 0755); err != nil {
		return hidden, err
	}
	hidden, err = openMountPoint(filepath.Join(root, "hidden"))
	if err != nil {
		return hidden, err
	}
	if err := mountNestedPayload(filepath.Join(root, "hidden"), "cover"); err != nil {
		return hidden, err
	}
	return hidden, os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link"))
}

func mountNestedPayload(target, value string) error {
	if err := unix.Mount("none", target, "tmpfs", 0, "mode=0755,size=64k"); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "payload"), []byte(value), 0755)
}

func cleanupNestedMountFixture(root string) error {
	// Every candidate is fixed beneath this fixture root in this private namespace.
	// Best-effort detach also handles failures between nested publication phases.
	var result error
	for _, name := range []string{"target/added", "target/child", "target", "source/child", "source", "hidden", "hidden/late", "hidden/buried", "hidden"} {
		err := unix.Unmount(filepath.Join(root, name), unix.MNT_DETACH)
		if err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			result = errors.Join(result, err)
		}
	}
	if err := unix.Mount("", root, "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		result = errors.Join(result, err)
	}
	if err := unix.Unmount(root, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) {
		result = errors.Join(result, err)
	}
	return errors.Join(result, os.RemoveAll(root))
}

func nestedRolesAlive(pins *fixtureLifecyclePins) error {
	for index, fd := range pins.pins {
		polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(polls, 0); err != nil {
			return err
		}
		if polls[0].Revents != 0 {
			return fmt.Errorf("captured nested role ended: role=%s pid=%d start=%d revents=%d", pins.roles[index].Role, pins.roles[index].PID, pins.roles[index].Start, polls[0].Revents)
		}
	}
	return nil
}

func nestedRolesEnded(pins *fixtureLifecyclePins) bool {
	for _, fd := range pins.pins {
		polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(polls, 0); err != nil || polls[0].Revents&unix.POLLIN == 0 || polls[0].Revents & ^int16(unix.POLLIN|unix.POLLHUP) != 0 {
			return false
		}
	}
	return true
}

func nestedRoleRecord(pins *fixtureLifecyclePins) string {
	events := [3]int16{}
	failed := false
	for index, fd := range pins.pins {
		polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(polls, 0); err != nil {
			failed = true
		}
		events[index] = polls[0].Revents
	}
	raw, err := json.Marshal(struct {
		Roles      []lifecycleRole `json:"roles"`
		Events     [3]int16        `json:"pidfd_revents"`
		PollFailed bool            `json:"poll_failed"`
		At         int64           `json:"monotonic_ns"`
	}{pins.roles, events, failed, lifecycleMonotonicNS()})
	if err != nil {
		return "unavailable"
	}
	return string(raw)
}

type nestedView struct {
	root     int
	members  int
	payloads map[string]string
	unsafe   bool
	hidden   bool
}

// The raw descriptor has one poll owner; epoch changes during payload/flag
// observations invalidate the entire sample before it can assert partial state.
func observeNestedView(target string) (state nestedView, changed bool, err error) {
	fd, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return state, false, err
	}
	defer unix.Close(fd)
	before, changed, err := readIntakeMountTopology(fd, readMountInfo)
	if err != nil || changed {
		return state, changed, err
	}
	state.payloads = make(map[string]string)
	for _, row := range before {
		if row.mountPoint != target && !strings.HasPrefix(row.mountPoint, target+"/") {
			continue
		}
		state.members++
		if row.mountPoint == target {
			state.root = row.id
		}
		var flags unix.Statfs_t
		if statErr := unix.Statfs(row.mountPoint, &flags); statErr != nil {
			err = errors.Join(err, statErr)
			continue
		}
		const required = unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV
		if flags.Flags&required != required {
			state.unsafe = true
		}
	}
	for _, name := range []string{"payload", "child/payload", "added/payload", "underlying"} {
		raw, readErr := os.ReadFile(filepath.Join(target, name))
		if readErr == nil {
			state.payloads[name] = string(raw)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			err = errors.Join(err, readErr)
		}
		if readErr == nil && name != "underlying" && !errors.Is(canMapExecutable(filepath.Join(target, name)), unix.EPERM) {
			state.unsafe = true
		}
	}
	for _, name := range []string{"buried", "late"} {
		if _, statErr := os.Stat(filepath.Join(target, name, "payload")); statErr == nil {
			state.hidden = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			err = errors.Join(err, statErr)
		}
	}
	changed, pollErr := mountInfoEpochChanged(fd)
	if changed {
		return nestedView{}, true, pollErr
	}
	return state, false, errors.Join(err, pollErr)
}

func nestedDesktopSample(source, desktop *os.File, target string) (state nestedView, changed bool, err error) {
	_, before, err := readCurrentMountTopology()
	if err != nil {
		return state, false, err
	}
	if err := unix.Setns(int(desktop.Fd()), unix.CLONE_NEWNS); err != nil {
		return state, false, err
	}
	state, changed, err = observeNestedView(target)
	restoreErr := unix.Setns(int(source.Fd()), unix.CLONE_NEWNS)
	if restoreErr != nil {
		return state, false, errors.Join(err, restoreErr)
	}
	_, after, sourceErr := readCurrentMountTopology()
	if sourceErr != nil {
		return state, false, errors.Join(err, sourceErr)
	}
	return state, changed || !sameMountEpoch(before, after, nil), err
}

func awaitNestedView(source, desktop *os.File, pins *fixtureLifecyclePins, target string, members, previous int, group bool, began int64, setupDeadline time.Time) (int, error) {
	deadline := time.Now().Add(mountSourceSetupLimit)
	if setupDeadline.Before(deadline) {
		deadline = setupDeadline
	}
	var firstAbsent, lastOld int64
	observations := 0
	for time.Now().Before(deadline) {
		if err := nestedRolesAlive(pins); err != nil {
			return 0, err
		}
		state, changed, err := nestedDesktopSample(source, desktop, target)
		if err != nil {
			return 0, err
		}
		if changed {
			continue
		}
		observations++
		at := lifecycleMonotonicNS()
		if state.unsafe || state.hidden {
			return 0, fmt.Errorf("unsafe stable nested sample: %+v", state)
		}
		complete := state.payloads["payload"] == "root" && state.payloads["child/payload"] == "child"
		expectedAdded := members == 3
		added := state.payloads["added/payload"] == "added"
		if state.root == 0 {
			if firstAbsent == 0 {
				firstAbsent = at
			}
			if group && (len(state.payloads) != 1 || state.payloads["underlying"] != "underlying") {
				return 0, errors.New("unavailable group exposed non-underlying payloads")
			}
		} else if group {
			wantMembers, wantAdded := members, expectedAdded
			if state.root == previous {
				wantMembers, wantAdded = 5-members, !expectedAdded
			}
			if !complete || state.members != wantMembers || added != wantAdded || state.payloads["underlying"] != "" {
				return 0, fmt.Errorf("stable partial group publication: %+v", state)
			}
		}
		if state.root == previous {
			lastOld = at
		}
		if complete && state.members == members && added == expectedAdded && (!group || state.root != previous) {
			fmt.Printf("PW_NESTED_VISIBILITY01 {\"action_ns\":%d,\"last_old_ns\":%d,\"first_absent_ns\":%d,\"complete_ns\":%d,\"observations\":%d,\"root_id\":%d,\"previous_root_id\":%d,\"members\":%d,\"group\":%t}\n", began, lastOld, firstAbsent, at, observations, state.root, previous, members, group)
			return state.root, nil
		}
		time.Sleep(time.Millisecond)
	}
	return 0, fmt.Errorf("nested complete view deadline: expected_members=%d previous_root=%d group=%t observations=%d", members, previous, group, observations)
}

func nestedExpectUnderlying(source, desktop *os.File, target string) error {
	state, changed, err := nestedDesktopSample(source, desktop, target)
	if err != nil {
		return err
	}
	if changed || state.root != 0 || state.members != 0 || state.payloads["underlying"] != "underlying" || len(state.payloads) != 1 {
		return fmt.Errorf("selected nested target has not returned to exact underlying state: %+v changed=%t", state, changed)
	}
	return nil
}

func checkNestedHiddenAndSymlink(source, desktop *os.File, pins *fixtureLifecyclePins, root string, expected int) (err error) {
	if err := nestedRolesAlive(pins); err != nil {
		return err
	}
	_, before, err := readCurrentMountTopology()
	if err != nil {
		return err
	}
	if err := unix.Setns(int(desktop.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unix.Setns(int(source.Fd()), unix.CLONE_NEWNS)) }()
	state, changed, err := observeNestedView(filepath.Join(root, "hidden"))
	if err != nil || changed {
		return errors.Join(err, errors.New("hidden control observation unstable"))
	}
	if state.root != expected || state.hidden || state.payloads["payload"] != "cover" || state.unsafe {
		return errors.New("covered underlay exposed or cover unsafe")
	}
	fd, openErr := openMountPoint(filepath.Join(root, "link"))
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	if !errors.Is(openErr, unix.ELOOP) {
		return fmt.Errorf("real destination lookup followed fixture symlink: %w", openErr)
	}
	redirected, changed, observeErr := observeNestedView(filepath.Join(root, "real"))
	if observeErr != nil || changed || redirected.members != 0 || len(redirected.payloads) != 0 {
		return errors.Join(observeErr, errors.New("symlink control acquired an alternate mount or payload"))
	}
	if err := unix.Setns(int(source.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	_, after, err := readCurrentMountTopology()
	if err != nil || !sameMountEpoch(before, after, nil) {
		return errors.New("source topology changed during hidden/symlink controls")
	}
	return nestedRolesAlive(pins)
}

// An owned tmpfs observation pin prevents mount-ID recycling during this one
// replacement. It is closed before source unmount and is not leak/latency evidence.
func pinNestedViewRoot(source, desktop *os.File, pins *fixtureLifecyclePins, target string, expected int) (fd int, err error) {
	fd = -1
	if err := nestedRolesAlive(pins); err != nil {
		return fd, err
	}
	birth, err := lifecycleBirth(pins.roles[0].PID)
	if err != nil || birth.Start != pins.roles[0].Start || birth.Parent != pins.roles[0].Parent {
		return -1, errors.New("desktop birth differs before view pin")
	}
	if err := unix.Setns(int(desktop.Fd()), unix.CLONE_NEWNS); err != nil {
		return -1, err
	}
	defer func() {
		err = errors.Join(err, unix.Setns(int(source.Fd()), unix.CLONE_NEWNS))
		after, birthErr := lifecycleBirth(pins.roles[0].PID)
		if birthErr != nil || after != birth {
			err = errors.Join(err, errors.New("desktop birth changed during view pin"))
		}
		err = errors.Join(err, nestedRolesAlive(pins))
		if err != nil && fd >= 0 {
			_ = unix.Close(fd)
			fd = -1
		}
	}()
	info, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(info)
	rows, changed, err := readIntakeMountTopology(info, readMountInfo)
	if err != nil || changed {
		return -1, errors.Join(err, errors.New("view pin topology unstable"))
	}
	selected := false
	for _, row := range rows {
		if row.id == expected && row.mountPoint == target {
			selected = true
		}
	}
	if !selected {
		return -1, errors.New("view pin topology lacks exact selected mount")
	}
	fd, err = openMountPoint(target)
	if err != nil {
		return -1, err
	}
	id, inode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return fd, err
	}
	changed, err = mountInfoEpochChanged(info)
	if err != nil || changed || id != uint64(expected) {
		return fd, errors.Join(err, errors.New("view pin changed exact mount identity"))
	}
	fmt.Printf("PW_NESTED_PIN01 {\"mount_id\":%d,\"inode\":%d,\"desktop_pid\":%d,\"desktop_start\":%d}\n", id, inode, birth.PID, birth.Start)
	return fd, nil
}

// The held descriptor selects only our own covered tmpfs, not a visible alias.
// A late nested mount here must remain excluded while later positive phases
// establish that the actual intake has reconciled this source epoch.
func publishNestedHiddenArrival(source, desktop *os.File, pins *fixtureLifecyclePins, root string, underlay int) (viewRoot int, err error) {
	actualNS, err := os.Stat("/proc/thread-self/ns/mnt")
	heldNS, heldErr := source.Stat()
	if err != nil || heldErr != nil || !os.SameFile(actualNS, heldNS) {
		return 0, errors.New("late hidden publication left exact owned source namespace")
	}
	baseline, changed, err := nestedDesktopSample(source, desktop, filepath.Join(root, "hidden"))
	if err != nil || changed || baseline.root == 0 || baseline.payloads["payload"] != "cover" || baseline.hidden {
		return 0, errors.Join(err, errors.New("hidden control lacks stable cover baseline"))
	}
	parent, _, err := mountDescriptorIdentity(underlay)
	if err != nil {
		return 0, err
	}
	target := fmt.Sprintf("/proc/self/fd/%d/late", underlay)
	if err := mountNestedPayload(target, "late-hidden"); err != nil {
		return 0, err
	}
	// Only this fixed child is opened relative to the already bound owned root.
	// The production resolver correctly refuses the proc-fd spelling above.
	fd, err := unix.Openat(underlay, "late", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, unix.Close(fd)) }()
	var object unix.Stat_t
	if err := unix.Fstat(fd, &object); err != nil {
		return 0, err
	}
	if object.Uid != 0 || object.Mode&unix.S_IFMT != unix.S_IFDIR {
		return 0, errors.New("late child is not the fixture-owned directory")
	}
	id, inode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return 0, err
	}
	if inode != object.Ino || id == parent {
		return 0, errors.New("late child descriptor differs from its new mount root")
	}
	_, rows, err := readCurrentMountTopology()
	if err != nil {
		return 0, err
	}
	hidden, err := hiddenMountRoots(rows)
	if err != nil {
		return 0, err
	}
	matched, parentMatched := false, false
	for _, row := range rows {
		if uint64(row.id) == parent && row.mountPoint == filepath.Join(root, "hidden") && hidden[row.id] {
			parentMatched = true
		}
		if uint64(row.id) == id && uint64(row.parent) == parent && hidden[row.id] {
			matched = true
		}
	}
	if !matched || !parentMatched {
		return 0, errors.New("late negative mount is not the exact covered source child")
	}
	if err := canMapExecutable(filepath.Join(target, "payload")); err != nil {
		return 0, fmt.Errorf("hidden source positive executable control: %w", err)
	}
	fmt.Printf("PW_NESTED_HIDDEN01 {\"parent_id\":%d,\"late_mount_id\":%d,\"late_root_inode\":%d,\"view_cover_id\":%d}\n", parent, id, inode, baseline.root)
	return baseline.root, checkNestedHiddenAndSymlink(source, desktop, pins, root, baseline.root)
}
