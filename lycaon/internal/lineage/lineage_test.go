package lineage_test

import (
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/lineage"
)

// sleeper starts a detached descendant holding the lineage descriptor.
func sleeper(t *testing.T, l *lineage.Lineage, setsid bool) *exec.Cmd {
	t.Helper()
	// The shell keeps the inherited descriptor open for its whole run.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.ExtraFiles = []*os.File{l.ChildFile()}
	if setsid {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start descendant: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestLineageObservesDescendants(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("descendant observation requires a supported platform")
	}
	l, err := lineage.Open("test", "")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	cmd := sleeper(t, l, false)
	l.Started()

	if !l.Live() {
		t.Fatal("lineage reports no descendants while one runs")
	}
	members, err := l.Survivors()
	if err != nil {
		t.Fatalf("survivors: %v", err)
	}
	if !slices.Contains(members, cmd.Process.Pid) {
		t.Fatalf("members %v omit the started descendant %d", members, cmd.Process.Pid)
	}
	owner, ok := lineage.Of(cmd.Process.Pid)
	if !ok || owner.ID() != l.ID() {
		t.Fatalf("Of(%d) did not resolve the owning lineage", cmd.Process.Pid)
	}
}

// A descendant that leaves the process group is still attributable.
func TestLineageSurvivesSetsid(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("descendant observation requires a supported platform")
	}
	l, err := lineage.Open("test", "")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	cmd := sleeper(t, l, true)
	l.Started()

	survivors, err := l.Survivors()
	if err != nil {
		t.Fatalf("survivors: %v", err)
	}
	if !slices.Contains(survivors, cmd.Process.Pid) {
		t.Fatalf("survivors %v omit the detached descendant %d", survivors, cmd.Process.Pid)
	}
}

func TestLineageEndsWithItsDescendants(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("descendant observation requires a supported platform")
	}
	l, err := lineage.Open("test", "")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.ExtraFiles = []*os.File{l.ChildFile()}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	l.Started()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for l.Live() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if l.Live() {
		t.Fatal("lineage still reports descendants after the only one exited")
	}
}

// A survivor stays nameable after its action ends, so a later refusal can say
// which action left it behind, and a server it runs still belongs to its session.
func TestRetiredLineageStillNamesItsSurvivors(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("descendant observation requires a supported platform")
	}
	l, err := lineage.Open("command:call_1", "session-1")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	cmd := sleeper(t, l, true)
	l.Started()
	if _, ok := lineage.Of(cmd.Process.Pid); !ok {
		t.Fatal("descendant not attributed while the action runs")
	}

	l.Retire()

	found, ok := lineage.Of(cmd.Process.Pid)
	if !ok {
		t.Fatal("retired lineage no longer names its surviving descendant")
	}
	if found.State() != lineage.StateRetired {
		t.Fatalf("survivor resolved to state %q, want retired", found.State())
	}
	if found.Owner() != "command:call_1" {
		t.Fatalf("survivor resolved to owner %q, want the action that started it", found.Owner())
	}
	if found.Session() != "session-1" {
		t.Fatalf("survivor resolved to session %q, want the session whose action started it", found.Session())
	}
}

func TestReleasedLineageResolvesNothing(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("descendant observation requires a supported platform")
	}
	l, err := lineage.Open("test", "")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.ExtraFiles = []*os.File{l.ChildFile()}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	l.Started()
	_ = cmd.Wait()
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, ok := lineage.Of(pid); ok {
		t.Fatal("released lineage still attributes a pid")
	}
}

// A retired lineage gives back its descriptor once its last descendant exits,
// rather than waiting to be evicted.
func TestRetiredLineageIsReclaimedWhenItsSurvivorsExit(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("descendant observation requires a supported platform")
	}
	l, err := lineage.Open("command:call_1", "")
	if err != nil {
		t.Fatalf("open lineage: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	cmd := sleeper(t, l, true)
	l.Started()
	if _, err := l.Survivors(); err != nil {
		t.Fatalf("survivors: %v", err)
	}
	pid := cmd.Process.Pid
	l.Retire()
	if _, ok := lineage.Of(pid); !ok {
		t.Fatal("retired lineage stopped naming its survivor too early")
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := lineage.Of(pid); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("retired lineage still holds its descriptor after its last descendant exited")
}
