//go:build unix

package command_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// TestBB05HelperProcess is the fixture-owned OS process used by M08 tests.
// In "parent" mode it spawns a "descendant" copy of itself that holds an flock
// so descendant settlement is directly observable without relying on PID reuse.
func TestBB05HelperProcess(t *testing.T) {
	mode := os.Getenv("PW_BB05_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "descendant":
		lockPath := os.Getenv("PW_BB05_LOCK")
		pidPath := os.Getenv("PW_BB05_DESC_PID")
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil || syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX) != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "parent":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(4)
		}
		desc := exec.Command(exe, "-test.run=^TestBB05HelperProcess$")
		desc.Env = append(os.Environ(), "PW_BB05_HELPER=descendant")
		if err := desc.Start(); err != nil {
			os.Exit(5)
		}
		descPIDPath := os.Getenv("PW_BB05_DESC_PID")
		for range 500 {
			if _, err := os.Stat(descPIDPath); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(os.Getenv("PW_BB05_PARENT_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(6)
		}
		fmt.Println("parent-ready")
		releasePath := os.Getenv("PW_BB05_RELEASE")
		for i := range 2000 {
			if releasePath != "" {
				if _, err := os.Stat(releasePath); err == nil {
					for j := range 16 {
						fmt.Printf("burst-%d\n", j)
					}
					_ = desc.Process.Kill()
					_, _ = desc.Process.Wait()
					os.Exit(0)
				}
			}
			if i%50 == 0 {
				fmt.Printf("tick-%d\n", i/50)
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = desc.Process.Kill()
		_, _ = desc.Process.Wait()
		os.Exit(0)
	}
}

func bb05LockFree(t *testing.T, lockPath string) bool {
	t.Helper()
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

// M08: Native job + fixture-owned helper OS process with observable descendant.
// Exercises output read vs exit race, parked wait interrupt, single-job stop,
// stable identity distinction (shell job token vs OS PID vs registry handle),
// descendant settlement, baseline cleanup, and session reuse under -race.
func TestBB05_M08_ProcessAndWaitReadRaces(t *testing.T) {
	exe, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)

	f := newProgramFixture(t)
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	parking := newRecordingParking()
	f.Runtime.Parking = parking

	lockPath := filepath.Join(f.Dir, "descendant.lock")
	parentPIDPath := filepath.Join(f.Dir, "parent.pid")
	descPIDPath := filepath.Join(f.Dir, "descendant.pid")

	// 1. Start a native job (%1) and a fixture-owned external helper OS process (%2) with a descendant.
	outNative, err := f.call(t, t.Context(), "sleep 60 & echo $!")
	testutil.FailErr(t, "start native job", err)
	nativeOutcome := decodeOutcome(t, outNative)
	nativePublicID, err := strconv.Atoi(strings.TrimSpace(nativeOutcome.Stdout))
	testutil.FailErr(t, "parse native $! id", err)

	helperCmd := fmt.Sprintf(
		"PW_BB05_HELPER=parent PW_BB05_LOCK=%s PW_BB05_PARENT_PID=%s PW_BB05_DESC_PID=%s external -- %s '-test.run=^TestBB05HelperProcess$' & echo $!",
		strconv.Quote(lockPath), strconv.Quote(parentPIDPath), strconv.Quote(descPIDPath), strconv.Quote(exe),
	)
	outExt, err := f.call(t, t.Context(), helperCmd)
	testutil.FailErr(t, "start external helper job", err)
	extOutcome := decodeOutcome(t, outExt)
	extHostPID, err := strconv.Atoi(strings.TrimSpace(extOutcome.Stdout))
	testutil.FailErr(t, "parse external $! host pid", err)

	testutil.WaitFor(t, "helper parent and descendant ready", func() bool {
		_, errP := os.Stat(parentPIDPath)
		_, errD := os.Stat(descPIDPath)
		return errP == nil && errD == nil
	})
	if bb05LockFree(t, lockPath) {
		t.Fatal("descendant did not hold its observable lock while running")
	}

	parentPIDBytes, err := os.ReadFile(parentPIDPath)
	testutil.FailErr(t, "read parent pid", err)
	descPIDBytes, err := os.ReadFile(descPIDPath)
	testutil.FailErr(t, "read descendant pid", err)
	parentPID, err := strconv.Atoi(strings.TrimSpace(string(parentPIDBytes)))
	testutil.FailErr(t, "parse parent pid", err)
	descPID, err := strconv.Atoi(strings.TrimSpace(string(descPIDBytes)))
	testutil.FailErr(t, "parse descendant pid", err)
	if parentPID <= 0 || descPID <= 0 || parentPID == descPID {
		t.Fatalf("invalid helper/descendant PIDs: parent=%d descendant=%d", parentPID, descPID)
	}

	// Verify stable distinction among shell job token (%1/%2), host process identity ($!),
	// synthetic native job ID (>= 1<<22), and bgprocess registry handle.
	if nativePublicID < (1 << 22) {
		t.Fatalf("native job $! = %d, want synthetic ID >= 1<<22", nativePublicID)
	}
	if extHostPID <= 0 || extHostPID >= (1<<22) {
		t.Fatalf("external helper $! = %d, want host OS PID in (0, 1<<22)", extHostPID)
	}
	active := f.Background.ActiveJobs(f.Session)
	if len(active) != 2 {
		t.Fatalf("active jobs = %d, want 2", len(active))
	}
	jobs := f.Runtime.Supervisor.Jobs(f.Session, "default")
	h1, err := jobs.Resolve("%1")
	testutil.FailErr(t, "resolve %1", err)
	h2, err := jobs.Resolve("%2")
	testutil.FailErr(t, "resolve %2", err)
	hByNativeID, err := jobs.Resolve(strconv.Itoa(nativePublicID))
	testutil.FailErr(t, "resolve native job by public ID", err)
	hByHostPID, err := jobs.Resolve(strconv.Itoa(extHostPID))
	testutil.FailErr(t, "resolve external helper by host OS PID", err)
	if h1 == h2 || h1 != hByNativeID || h2 != hByHostPID {
		t.Fatalf("job token / handle / PID mismatch: %%1=%q(id=%d->%q) %%2=%q(pid=%d->%q)", h1, nativePublicID, hByNativeID, h2, extHostPID, hByHostPID)
	}
	if h1 == "%1" || h2 == "%2" || h2 == strconv.Itoa(extHostPID) {
		t.Fatalf("registry handles collapsed into shell tokens or OS PIDs: h1=%q h2=%q", h1, h2)
	}

	// 2. Interrupt a parked wait on %1; verify wait settles interrupted while %1 and %2 keep running.
	waitDone := make(chan callResult, 1)
	go func() {
		content, callErr := f.call(t, t.Context(), "wait %1")
		waitDone <- callResult{content, callErr}
	}()
	testutil.WaitFor(t, "parked wait timer scheduled", func() bool {
		return clock.Pending() >= 2
	})
	clock.Advance(toolcontract.ProgramParkThreshold)
	parkedRes := <-waitDone
	testutil.FailErr(t, "park wait %1", parkedRes.err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked wait", json.Unmarshal([]byte(parkedRes.content), &parked))
	if parked.State != "parked" {
		t.Fatalf("expected parked wait, got %+v", parked)
	}
	recPark := <-parking.parked
	interruptedRaw := recPark.park.Interrupt(t.Context())
	interrupted := decodeOutcome(t, interruptedRaw)
	if interrupted.State != "interrupted" || interrupted.Status != 130 || interrupted.StopCode != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("interrupted parked wait = %+v", interrupted)
	}
	<-recPark.settled
	if len(f.Background.ActiveJobs(f.Session)) != 2 {
		t.Fatalf("interrupting parked wait killed background jobs: active=%d", len(f.Background.ActiveJobs(f.Session)))
	}

	// 3. Stop only the external helper job (%2) while concurrently racing output reads under -race.
	var readerWG sync.WaitGroup
	var readErrs atomic.Int32
	for range 4 {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			var cursor int64
			for range 64 {
				snap, rErr := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, h2, cursor)
				if rErr != nil || snap.Output.Next < cursor {
					readErrs.Add(1)
					return
				}
				cursor = snap.Output.Next
				if !snap.Output.Running && snap.Output.ExitCode != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	_, err = f.Background.Lifecycle.Stop(f.Session, h2, bgprocess.TerminationStoppedByAgent)
	testutil.FailErr(t, "stop external helper job", err)
	_, _, err = f.Background.Lifecycle.Wait(t.Context(), f.Session, h2)
	testutil.FailErr(t, "wait stopped external helper job", err)
	readerWG.Wait()
	if readErrs.Load() != 0 {
		t.Fatalf("concurrent output read/exit race failed with %d errors", readErrs.Load())
	}

	// Observed descendant must settle and release its lock; sibling native job (%1) must remain active.
	testutil.WaitFor(t, "descendant process settled", func() bool {
		return bb05LockFree(t, lockPath)
	})
	remaining := f.Background.ActiveJobs(f.Session)
	if len(remaining) != 1 || remaining[0].Handle != h1 {
		t.Fatalf("stopping %%2 affected sibling %%1: remaining=%+v", remaining)
	}

	// 4. Stop the remaining native job and verify capacity/waiters return to baseline.
	_, err = f.Background.Lifecycle.Stop(f.Session, h1, bgprocess.TerminationStoppedByAgent)
	testutil.FailErr(t, "stop native job", err)
	_, _, err = f.Background.Lifecycle.Wait(t.Context(), f.Session, h1)
	testutil.FailErr(t, "wait stopped native job", err)
	if activeAfter := f.Background.ActiveJobs(f.Session); len(activeAfter) != 0 {
		t.Fatalf("capacity did not return to baseline: active=%+v", activeAfter)
	}

	// 5. Race output read with a clean exit of a short-lived helper OS process.
	releaseFile := filepath.Join(f.Dir, "release-clean")
	cleanLock := filepath.Join(f.Dir, "clean-desc.lock")
	cleanParentPID := filepath.Join(f.Dir, "clean-parent.pid")
	cleanDescPID := filepath.Join(f.Dir, "clean-desc.pid")
	cleanCmd := fmt.Sprintf(
		"PW_BB05_HELPER=parent PW_BB05_LOCK=%s PW_BB05_PARENT_PID=%s PW_BB05_DESC_PID=%s PW_BB05_RELEASE=%s external -- %s '-test.run=^TestBB05HelperProcess$' &",
		strconv.Quote(cleanLock), strconv.Quote(cleanParentPID), strconv.Quote(cleanDescPID), strconv.Quote(releaseFile), strconv.Quote(exe),
	)
	_, err = f.call(t, t.Context(), cleanCmd)
	testutil.FailErr(t, "start clean-exit helper", err)
	testutil.WaitFor(t, "clean-exit helper ready", func() bool {
		_, errP := os.Stat(cleanParentPID)
		return errP == nil
	})
	cleanJobs := f.Background.ActiveJobs(f.Session)
	if len(cleanJobs) != 1 {
		t.Fatalf("expected 1 clean-exit helper job, got %d", len(cleanJobs))
	}
	cleanHandle := cleanJobs[0].Handle
	var cleanWG sync.WaitGroup
	var cleanReadFailed atomic.Bool
	for range 4 {
		cleanWG.Add(1)
		go func() {
			defer cleanWG.Done()
			var cur int64
			for range 128 {
				snap, rErr := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, cleanHandle, cur)
				if rErr != nil || snap.Output.Next < cur {
					cleanReadFailed.Store(true)
					return
				}
				cur = snap.Output.Next
				if !snap.Output.Running && snap.Output.ExitCode != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	testutil.FailErr(t, "release clean-exit helper", os.WriteFile(releaseFile, nil, 0o600))
	exitCode, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, cleanHandle)
	testutil.FailErr(t, "wait clean-exit helper", err)
	cleanWG.Wait()
	if exitCode != 0 || cleanReadFailed.Load() {
		t.Fatalf("clean-exit output/exit race: exit=%d readFailed=%v", exitCode, cleanReadFailed.Load())
	}
	testutil.WaitFor(t, "clean-exit descendant settled", func() bool {
		return bb05LockFree(t, cleanLock)
	})

	// 6. Verify subsequent session reuse succeeds cleanly.
	reusedRaw, err := f.call(t, t.Context(), "echo session-reused-ok")
	testutil.FailErr(t, "reuse session after job stops", err)
	reused := decodeOutcome(t, reusedRaw)
	if reused.State != "completed" || reused.Status != 0 || reused.Stdout != "session-reused-ok\n" {
		t.Fatalf("session reuse outcome = %+v", reused)
	}
}
