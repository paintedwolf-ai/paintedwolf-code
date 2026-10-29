//go:build linux

package launchguard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestMountLateNestedSubtreeRefreshPreservesSafeVisibility(t *testing.T) {
	fixture := installLaunchFixture(t)
	groups, err := os.Getgroups()
	testutil.FailErr(t, "capture actual caller groups", err)
	encoded, err := json.Marshal(groups)
	testutil.FailErr(t, "encode actual caller groups", err)
	err = sudoFixture(t, "mount-nested", fixture.dir,
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid()),
		"PW_NESTED_GROUPS="+string(encoded))
	testutil.FailErr(t, "real private late nested publication/removal", err)
}

// This fixture is root only inside its own private source namespace. The real
// setuid bootstrap starts with the original caller's complete credentials.
func checkLateNestedMounts(directory string) (err error) {
	if err := enterOwnedPrivateTopologyNamespace(); err != nil {
		return err
	}
	uid, uidErr := strconv.ParseUint(os.Getenv(fixtureUIDEnv), 10, 32)
	gid, gidErr := strconv.ParseUint(os.Getenv(fixtureGIDEnv), 10, 32)
	var groups []uint32
	if uidErr != nil || gidErr != nil || uid == 0 || json.Unmarshal([]byte(os.Getenv("PW_NESTED_GROUPS")), &groups) != nil {
		return errors.New("invalid captured nested fixture caller")
	}
	root := filepath.Join(directory, "late-nested")
	if err := os.Mkdir(root, 0755); err != nil {
		return err
	}
	cleanupAllowed := true
	underlay := -1
	defer func() {
		if underlay >= 0 {
			err = errors.Join(err, unix.Close(underlay))
			underlay = -1
		}
		if cleanupAllowed {
			err = errors.Join(err, cleanupNestedMountFixture(root))
		} else {
			err = errors.Join(err, fmt.Errorf("owned nested cleanup incomplete; retained root %s", root))
		}
	}()
	if err := shareOwnedNestedRoot(root); err != nil {
		return err
	}
	if underlay, err = prepareNestedMountFixture(root, int(uid), int(gid)); err != nil {
		return err
	}
	if err = canMapExecutable(filepath.Join(root, "source", "payload")); err != nil {
		return fmt.Errorf("ordinary source is not executable: %w", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	launch, startErr := startNestedCaller(directory, root, uint32(uid), uint32(gid), groups)
	if launch.cmd == nil {
		return startErr
	}
	cleanupAllowed = false
	var pins *fixtureLifecyclePins
	defer func() {
		if underlay >= 0 {
			err = errors.Join(err, unix.Close(underlay))
			underlay = -1
		}
		fmt.Printf("PW_NESTED_PRECLOSE01 %d\n", lifecycleMonotonicNS())
		if pins != nil {
			fmt.Printf("PW_NESTED_ROLES01 %s\n", nestedRoleRecord(pins))
			defer pins.close()
		}
		err = errors.Join(err, launch.stdin.Close())
		cleanupDeadline := time.Now().Add(25 * time.Second)
		select {
		case <-launch.result.done:
		case <-time.After(20 * time.Second):
			err = errors.Join(err, errors.New("owned bootstrap did not settle after input close"), launch.cmd.Process.Kill())
		}
		select {
		case <-launch.result.done:
			err = errors.Join(err, launch.result.err)
			fmt.Printf("PW_NESTED_STDERR01 %s\n", launch.stderr.String())
			cleanupAllowed = pins == nil && launch.result.err == nil
			for pins != nil && time.Now().Before(cleanupDeadline) {
				if nestedRolesEnded(pins) {
					cleanupAllowed = true
					fmt.Printf("PW_NESTED_SETTLED01 %s\n", nestedRoleRecord(pins))
					break
				}
				time.Sleep(time.Millisecond)
			}
		case <-time.After(time.Until(cleanupDeadline)):
			err = errors.Join(err, errors.New("owned bootstrap original Wait remains unsettled"))
		}
	}()
	if startErr != nil {
		return startErr
	}
	for key, value := range map[string]string{
		fixtureDesktopPIDEnv:       strconv.Itoa(launch.facts.PID),
		"PW_FIXTURE_DESKTOP_START": strconv.FormatUint(launch.facts.ProcessStarts[launch.facts.PID], 10),
		"PW_FIXTURE_BOOTSTRAP_PID": strconv.Itoa(launch.cmd.Process.Pid),
	} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	pins, err = captureFixtureLifecyclePins()
	if err != nil {
		return err
	}
	if err := nestedRolesAlive(pins); err != nil {
		return err
	}
	desktopBefore, err := lifecycleBirth(launch.facts.PID)
	if err != nil || desktopBefore.Start != pins.roles[0].Start || desktopBefore.Parent != pins.roles[0].Parent {
		return errors.New("desktop birth changed before namespace capture")
	}
	desktopNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", launch.facts.PID))
	if err != nil {
		return err
	}
	defer desktopNS.Close()
	desktopAfter, err := lifecycleBirth(launch.facts.PID)
	if err != nil || desktopBefore != desktopAfter {
		return errors.New("desktop birth changed across namespace capture")
	}
	if err = nestedRolesAlive(pins); err != nil {
		return err
	}
	sourceNS, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return err
	}
	defer sourceNS.Close()
	ownedUnderlay := underlay
	underlay = -1
	return checkNestedTopologyChanges(directory, root, ownedUnderlay, pins, sourceNS, desktopNS, deadline)
}

// This phase owns its held source root while exercising live arrival, replacement and removal.
func checkNestedTopologyChanges(directory, root string, underlay int, pins *fixtureLifecyclePins, sourceNS, desktopNS *os.File, deadline time.Time) (err error) {
	defer func() {
		if underlay >= 0 {
			err = errors.Join(err, unix.Close(underlay))
		}
	}()
	target := filepath.Join(root, "target")
	if err := nestedExpectUnderlying(sourceNS, desktopNS, target); err != nil {
		return err
	}
	began := lifecycleMonotonicNS()
	if err := unix.Mount(filepath.Join(root, "source"), target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return err
	}
	probe, err := probeNestedSingleTree(directory, pins)
	if err != nil {
		return err
	}
	group := probe.Errno == int(unix.EINVAL) && probe.Members > 1
	rawProof, encodeErr := json.Marshal(probe)
	if encodeErr != nil {
		return encodeErr
	}
	fmt.Printf("PW_NESTED_PROBE01 %s\n", rawProof)
	fmt.Printf("PW_NESTED_GROUP01 {\"single_errno\":%d,\"members\":%d,\"group_exercised\":%t}\n", probe.Errno, probe.Members, group)
	first, err := awaitNestedView(sourceNS, desktopNS, pins, target, 2, 0, group, began, deadline)
	if err != nil {
		return err
	}
	held := -1
	defer func() {
		if held >= 0 {
			err = errors.Join(err, unix.Close(held))
		}
	}()
	hiddenCover, err := publishNestedHiddenArrival(sourceNS, desktopNS, pins, root, underlay)
	if err != nil {
		return err
	}
	closeHiddenErr := unix.Close(underlay)
	underlay = -1
	if closeHiddenErr != nil {
		return closeHiddenErr
	}
	if group {
		held, err = pinNestedViewRoot(sourceNS, desktopNS, pins, target, first)
		if err != nil {
			return err
		}
	}
	began = lifecycleMonotonicNS()
	if err := mountNestedPayload(filepath.Join(target, "added"), "added"); err != nil {
		return err
	}
	added, err := awaitNestedView(sourceNS, desktopNS, pins, target, 3, first, group, began, deadline)
	if err != nil {
		return err
	}
	if held >= 0 {
		closeErr := unix.Close(held)
		held = -1
		if closeErr != nil {
			return closeErr
		}
	}
	if group {
		held, err = pinNestedViewRoot(sourceNS, desktopNS, pins, target, added)
		if err != nil {
			return err
		}
	}
	began = lifecycleMonotonicNS()
	if err := unix.Unmount(filepath.Join(target, "added"), 0); err != nil {
		return err
	}
	if _, err = awaitNestedView(sourceNS, desktopNS, pins, target, 2, added, group, began, deadline); err != nil {
		return err
	}
	if held >= 0 {
		closeErr := unix.Close(held)
		held = -1
		if closeErr != nil {
			return closeErr
		}
	}
	if err := checkNestedHiddenAndSymlink(sourceNS, desktopNS, pins, root, hiddenCover); err != nil {
		return err
	}
	// The recursive publication owns the visible child separately in this namespace.
	if err := unix.Unmount(filepath.Join(target, "child"), 0); err != nil {
		return err
	}
	if err := unix.Unmount(target, 0); err != nil {
		return err
	}
	for time.Now().Before(deadline) {
		if err = nestedRolesAlive(pins); err != nil {
			return err
		}
		if err = nestedExpectUnderlying(sourceNS, desktopNS, target); err == nil {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("selected nested target did not detach: %w", err)
}

func startNestedCaller(directory, root string, uid, gid uint32, groups []uint32) (launch runningLaunch, err error) {
	cmd := exec.Command(filepath.Join(directory, "pw-launch"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: groups}}
	cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.WaitDelay = 5 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return launch, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return launch, err
	}
	reader := bufio.NewReader(output)
	stderr := new(strings.Builder)
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return launch, err
	}
	result := &launchWaitResult{done: make(chan struct{})}
	go func() { result.err = cmd.Wait(); close(result.done) }()
	launch = runningLaunch{cmd: cmd, stdin: input, reader: reader, result: result, stderr: stderr}
	line := make(chan string, 1)
	go func() { text, _ := reader.ReadString('\n'); line <- text }()
	select {
	case text := <-line:
		if err = json.Unmarshal([]byte(text), &launch.facts); err != nil {
			return launch, fmt.Errorf("decode actual nested desktop: %w", err)
		}
	case <-time.After(mountSourceSetupLimit):
		return launch, errors.New("nested desktop readiness deadline")
	}
	if launch.facts.UIDs != [3]int{int(uid), int(uid), int(uid)} || launch.facts.GIDs != [3]int{int(gid), int(gid), int(gid)} {
		return launch, errors.New("nested desktop did not retain captured nonroot identity")
	}
	return launch, nil
}

type nestedGroupProof struct {
	Errno     int    `json:"errno"`
	Members   int    `json:"members"`
	RootID    int    `json:"root_id"`
	RootInode uint64 `json:"root_inode"`
	MemberIDs []int  `json:"member_ids"`
}

func probeNestedSingleTree(directory string, pins *fixtureLifecyclePins) (proof nestedGroupProof, err error) {
	intake := pins.roles[2]
	before, err := lifecycleBirth(intake.PID)
	if err != nil || before.Start != intake.Start || before.Parent != intake.Parent {
		return proof, errors.New("intake birth changed before group probe")
	}
	user, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", intake.PID))
	if err != nil {
		return proof, err
	}
	defer user.Close()
	mount, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", intake.PID))
	if err != nil {
		return proof, err
	}
	defer mount.Close()
	if err = nestedRolesAlive(pins); err != nil {
		return proof, err
	}
	after, err := lifecycleBirth(intake.PID)
	if err != nil || before != after {
		return proof, errors.New("intake birth changed across namespace capture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nsenter", "--user=/proc/self/fd/3", "--mount=/proc/self/fd/4", "--preserve-credentials", "--", os.Args[0], "-test.run=^TestProtectedLaunchFixture$")
	cmd.ExtraFiles = []*os.File{user, mount}
	cmd.Env = []string{"PATH=/usr/bin:/bin", fixtureModeEnv + "=mount-nested-probe", fixtureDirEnv + "=" + directory}
	cmd.WaitDelay = time.Second
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	output, err := cmd.Output()
	if err != nil {
		return proof, fmt.Errorf("actual SINGLE probe: %w: stdout=%s stderr=%s", err, output, diagnostics.Bytes())
	}
	if diagnostics.Len() != 0 {
		_, _ = fmt.Fprintf(os.Stderr, "PW_NESTED_PROBE_STDERR01 %s\n", diagnostics.Bytes())
	}
	if err := json.Unmarshal(output, &proof); err != nil {
		return proof, err
	}
	after, err = lifecycleBirth(intake.PID)
	if err != nil || before != after {
		return proof, errors.New("intake birth changed during group probe")
	}
	if proof.Members < 2 || len(proof.MemberIDs) != proof.Members || proof.RootID <= 0 || proof.RootInode == 0 || !slices.Contains(proof.MemberIDs, proof.RootID) || (proof.Errno != 0 && proof.Errno != int(unix.EINVAL)) {
		return proof, errors.New("group probe lacks actual complete subtree or supported SINGLE outcome")
	}
	return proof, nestedRolesAlive(pins)
}

func runNestedSingleProbe(directory string) error {
	target := filepath.Join(directory, "late-nested", "target")
	_, before, err := readCurrentMountTopology()
	if err != nil {
		return err
	}
	fd, err := openMountPoint(target)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	id, inode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return err
	}
	rootID := int(id)
	selected := false
	for _, row := range before {
		if row.id == rootID && row.mountPoint == target {
			selected = true
		}
	}
	if !selected {
		return errors.New("SINGLE probe does not hold exact selected root")
	}
	members := indexMountForest(before).subtree(rootID)
	ids := make([]int, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	tree, cloneErr := unix.OpenTree(fd, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
	if tree >= 0 {
		_ = unix.Close(tree)
	}
	_, after, err := readCurrentMountTopology()
	if err != nil || !sameMountEpoch(before, after, nil) {
		return errors.New("SINGLE probe source epoch changed")
	}
	var errno unix.Errno
	if cloneErr != nil && !errors.As(cloneErr, &errno) {
		return cloneErr
	}
	return json.NewEncoder(os.Stdout).Encode(nestedGroupProof{Errno: int(errno), Members: len(members), RootID: rootID, RootInode: inode, MemberIDs: ids})
}
