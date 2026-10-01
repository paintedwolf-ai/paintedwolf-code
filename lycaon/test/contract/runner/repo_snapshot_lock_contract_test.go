package contract

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func snapshotLockScript(t *testing.T) string {
	t.Helper()
	return filepath.Join(contractcheck.RepoRoot(t), "scripts", "repo-snapshot-lock.sh")
}

func snapshotPublishLib(t *testing.T) string {
	t.Helper()
	return filepath.Join(contractcheck.RepoRoot(t), "scripts", "snapshot-publish.sh")
}

func snapshotEpochPath(lockRoot string) string {
	return filepath.Join(lockRoot, "repo-snapshot.lockdir", "epoch")
}

func snapshotEpoch(t *testing.T, lockRoot string) string {
	t.Helper()
	data, err := os.ReadFile(snapshotEpochPath(lockRoot))
	if os.IsNotExist(err) {
		return "0"
	}
	contractcheck.FailErr(t, "read snapshot epoch", err)
	return strings.TrimSpace(string(data))
}

func snapshotCmd(t *testing.T, lockRoot string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{snapshotLockScript(t)}, args...)...)
	cmd.Dir = contractcheck.RepoRoot(t)
	// Scrub any hold token inherited from the digest's own read wrapper so the
	// invocation under test is top-level, not a nested inline run.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PW_REPO_SNAPSHOT_TOKEN=") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "PW_LOCK_ROOT="+lockRoot)
	return cmd
}

// snapshotPublish installs content at dest through the publish helper, the way
// generator scripts do, under an exclusive writer hold.
func snapshotPublish(t *testing.T, lockRoot, dest, content string) {
	t.Helper()
	script := "source '" + snapshotPublishLib(t) + "'\n" +
		"staged=\"$SNAPSHOT_LOCK_DIR/staged.$$\"\n" +
		"mkdir -p \"$SNAPSHOT_LOCK_DIR\"\n" +
		"printf '%s' '" + content + "' >\"$staged\"\n" +
		"snapshot_publish_file \"$staged\" '" + dest + "'\n" +
		"snapshot_publish_finish\n"
	cmd := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("publish %q to %s: %v\n%s", content, dest, err, out)
	}
}

func waitForFile(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared at %s", what, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitMarker is the reader-side half of the deterministic handshakes below: a
// command that records one attempt and blocks until the test raises a marker.
func awaitMarker(attempts, marker string) string {
	return "echo run >>'" + attempts + "'; for _ in $(seq 300); do [ -f '" + marker + "' ] && exit 0; sleep 0.1; done; exit 90"
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	contractcheck.FailErr(t, "read "+path, err)
	return strings.Count(string(data), "\n")
}

func TestRepositorySnapshotLockRequiresMode(t *testing.T) {
	cmd := snapshotCmd(t, t.TempDir(), "bash", "-c", "true")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected usage error without generate|read")
	}
	if !bytes.Contains(out, []byte("usage:")) {
		t.Fatalf("usage error: %s", out)
	}
}

func TestRepositorySnapshotLockRecoversDeadWriter(t *testing.T) {
	lockRoot := t.TempDir()
	lockDir := filepath.Join(lockRoot, "repo-snapshot.lockdir")
	writerFile := filepath.Join(lockDir, "writer")
	contractcheck.FailErr(t, "create stale snapshot lock", os.MkdirAll(lockDir, 0o700))
	contractcheck.FailErr(t, "write stale snapshot writer", os.WriteFile(
		writerFile, []byte("99999999\nstale-token\nstale-start\nstale command\n"), 0o600,
	))

	cmd := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c", "true")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recover stale writer: %v\n%s", err, output)
	}
	if _, err := os.Stat(writerFile); !os.IsNotExist(err) {
		t.Fatalf("stale writer remains after command: %v", err)
	}
}

func TestRepositorySnapshotLockWritersSerialize(t *testing.T) {
	lockRoot := t.TempDir()
	started := filepath.Join(lockRoot, "w1-started")
	release := filepath.Join(lockRoot, "w1-release")
	second := filepath.Join(lockRoot, "w2-ran")

	w1 := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c",
		"touch '"+started+"'; for _ in $(seq 300); do [ -f '"+release+"' ] && exit 0; sleep 0.1; done; exit 90")
	contractcheck.FailErr(t, "start first writer", w1.Start())
	waitForFile(t, started, "first writer gate")

	w2 := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c", "touch '"+second+"'")
	contractcheck.FailErr(t, "start second writer", w2.Start())

	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(second); err == nil {
		t.Fatal("second writer ran while the first held the writer slot")
	}
	contractcheck.FailErr(t, "release first writer", os.WriteFile(release, nil, 0o600))
	contractcheck.FailErr(t, "wait first writer", w1.Wait())
	contractcheck.FailErr(t, "wait second writer", w2.Wait())
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("second writer never ran after release: %v", err)
	}
}

func TestRepositorySnapshotLockGenerateDoesNotWaitForReader(t *testing.T) {
	lockRoot := t.TempDir()
	readerStarted := filepath.Join(lockRoot, "reader-started")
	writerDone := filepath.Join(lockRoot, "writer-done")
	attempts := filepath.Join(lockRoot, "attempts")

	reader := snapshotCmd(t, lockRoot, "read", "--", "bash", "-c",
		"touch '"+readerStarted+"'; "+awaitMarker(attempts, writerDone))
	contractcheck.FailErr(t, "start reader", reader.Start())
	waitForFile(t, readerStarted, "reader gate")

	started := time.Now()
	writer := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c", "touch '"+writerDone+"'")
	if out, err := writer.CombinedOutput(); err != nil {
		_ = reader.Process.Kill()
		t.Fatalf("generate under live reader: %v\n%s", err, out)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("generate blocked on live reader for %s", elapsed)
	}
	contractcheck.FailErr(t, "wait reader", reader.Wait())
	if got := countLines(t, attempts); got != 1 {
		t.Fatalf("reader ran %d attempts; a publish-free writer must not invalidate it", got)
	}
}

func TestRepositorySnapshotLockReaderRerunsOnRealPublish(t *testing.T) {
	lockRoot := t.TempDir()
	dest := filepath.Join(lockRoot, "generated.txt")
	contractcheck.FailErr(t, "seed generated file", os.WriteFile(dest, []byte("old"), 0o600))
	published := filepath.Join(lockRoot, "published")
	attempts := filepath.Join(lockRoot, "attempts")

	reader := snapshotCmd(t, lockRoot, "read", "--", "bash", "-c", awaitMarker(attempts, published))
	var readerOut bytes.Buffer
	reader.Stdout = &readerOut
	reader.Stderr = &readerOut
	contractcheck.FailErr(t, "start reader", reader.Start())
	waitForFile(t, attempts, "reader first attempt")

	snapshotPublish(t, lockRoot, dest, "new")
	contractcheck.FailErr(t, "raise published marker", os.WriteFile(published, nil, 0o600))

	if err := reader.Wait(); err != nil {
		t.Fatalf("invalidated reader must rerun and succeed: %v\n%s", err, readerOut.String())
	}
	if got := countLines(t, attempts); got != 2 {
		t.Fatalf("reader ran %d attempts, want 2 (one invalidated, one clean)\n%s", got, readerOut.String())
	}
}

func TestRepositorySnapshotLockReaderIgnoresNoopPublish(t *testing.T) {
	lockRoot := t.TempDir()
	dest := filepath.Join(lockRoot, "generated.txt")
	contractcheck.FailErr(t, "seed generated file", os.WriteFile(dest, []byte("same"), 0o600))
	published := filepath.Join(lockRoot, "published")
	attempts := filepath.Join(lockRoot, "attempts")

	reader := snapshotCmd(t, lockRoot, "read", "--", "bash", "-c", awaitMarker(attempts, published))
	contractcheck.FailErr(t, "start reader", reader.Start())
	waitForFile(t, attempts, "reader first attempt")

	snapshotPublish(t, lockRoot, dest, "same")
	contractcheck.FailErr(t, "raise published marker", os.WriteFile(published, nil, 0o600))

	contractcheck.FailErr(t, "wait reader", reader.Wait())
	if got := countLines(t, attempts); got != 1 {
		t.Fatalf("reader ran %d attempts; identical bytes must not invalidate it", got)
	}
	if content, err := os.ReadFile(dest); err != nil || string(content) != "same" {
		t.Fatalf("generated file disturbed by no-op publish: %q %v", content, err)
	}
}

func TestRepositorySnapshotLockReaderFailsAfterRepeatedInvalidation(t *testing.T) {
	lockRoot := t.TempDir()
	dest := filepath.Join(lockRoot, "generated.txt")
	contractcheck.FailErr(t, "seed generated file", os.WriteFile(dest, []byte("v0"), 0o600))
	attempts := filepath.Join(lockRoot, "attempts")

	reader := snapshotCmd(t, lockRoot, "read", "--", "bash", "-c",
		"echo run >>'"+attempts+"'; runs=$(($(wc -l <'"+attempts+"'))); for _ in $(seq 300); do [ -f '"+lockRoot+"/go-'\"$runs\" ] && exit 0; sleep 0.1; done; exit 90")
	reader.Env = append(reader.Env, "PW_SNAPSHOT_READ_ATTEMPTS=2")
	var readerOut bytes.Buffer
	reader.Stdout = &readerOut
	reader.Stderr = &readerOut
	contractcheck.FailErr(t, "start reader", reader.Start())

	for attempt := 1; attempt <= 2; attempt++ {
		waitForFile(t, attempts, "reader attempt")
		deadline := time.Now().Add(20 * time.Second)
		for countLines(t, attempts) < attempt {
			if time.Now().After(deadline) {
				t.Fatalf("reader attempt %d never started\n%s", attempt, readerOut.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
		snapshotPublish(t, lockRoot, dest, "v"+string(rune('0'+attempt)))
		contractcheck.FailErr(t, "release attempt", os.WriteFile(filepath.Join(lockRoot, "go-"+string(rune('0'+attempt))), nil, 0o600))
	}

	if err := reader.Wait(); err == nil {
		t.Fatalf("reader must fail once every attempt was invalidated\n%s", readerOut.String())
	}
	if !strings.Contains(readerOut.String(), "kept changing") {
		t.Fatalf("exhaustion must be reported plainly, got:\n%s", readerOut.String())
	}
}

func TestRepositorySnapshotLockReaderRecoversAbandonedPublishWindow(t *testing.T) {
	lockRoot := t.TempDir()
	contractcheck.FailErr(t, "create lock dir", os.MkdirAll(filepath.Dir(snapshotEpochPath(lockRoot)), 0o700))
	contractcheck.FailErr(t, "leave publish window open", os.WriteFile(snapshotEpochPath(lockRoot), []byte("5\n"), 0o600))

	cmd := snapshotCmd(t, lockRoot, "read", "--", "bash", "-c", "true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reader must recover an abandoned publish window: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("publish window")) {
		t.Fatalf("recovery must be reported, got: %s", out)
	}
	if got := snapshotEpoch(t, lockRoot); got != "6" {
		t.Fatalf("epoch left at %s, want the window forced closed at 6", got)
	}
}

func TestRepositorySnapshotLockWriterTimeoutKillsAndReleases(t *testing.T) {
	lockRoot := t.TempDir()
	cmd := snapshotCmd(t, lockRoot, "generate", "--", "sleep", "60")
	cmd.Env = append(cmd.Env, "PW_SNAPSHOT_WRITER_TIMEOUT=1")
	started := time.Now()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("timed-out writer must fail\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 124 {
		t.Fatalf("timed-out writer exit: %v (want 124)\n%s", err, out)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("timeout enforcement took %s", elapsed)
	}
	if !bytes.Contains(out, []byte("exceeded")) {
		t.Fatalf("timeout must be reported, got: %s", out)
	}

	next := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c", "true")
	if nextOut, nextErr := next.CombinedOutput(); nextErr != nil {
		t.Fatalf("writer slot not released after timeout: %v\n%s", nextErr, nextOut)
	}
}

func TestRepositorySnapshotLockIsReentrantForChildCommands(t *testing.T) {
	lockRoot := t.TempDir()
	script := snapshotLockScript(t)
	cmd := snapshotCmd(t, lockRoot, "generate", "--",
		"bash", script, "read", "--",
		"bash", script, "generate", "--", "bash", "-c", "true")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nested generate/read/generate: %v\n%s", err, output)
	}

	nestedRead := snapshotCmd(t, lockRoot, "read", "--", "bash", script, "read", "--", "bash", "-c", "true")
	if output, err := nestedRead.CombinedOutput(); err != nil {
		t.Fatalf("nested read/read: %v\n%s", err, output)
	}
}

func TestRepositorySnapshotLockHoldingReportsToken(t *testing.T) {
	lockRoot := t.TempDir()
	script := snapshotLockScript(t)
	cmd := snapshotCmd(t, lockRoot, "read", "--", "bash", script, "holding")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("holding inside read: %v\n%s", err, out)
	}
	outside := snapshotCmd(t, lockRoot, "holding")
	if err := outside.Run(); err == nil {
		t.Fatal("holding without a token must fail")
	}
}

func TestRepositorySnapshotLockStatusReportsState(t *testing.T) {
	lockRoot := t.TempDir()
	out, err := snapshotCmd(t, lockRoot, "status").CombinedOutput()
	contractcheck.FailErr(t, "status on idle lock", err)
	for _, want := range []string{"epoch: 0 (settled)", "writer: none", "waiters: none"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
}

func TestRepositorySnapshotLockTerminationStopsProtectedCommand(t *testing.T) {
	lockRoot := t.TempDir()
	started := filepath.Join(lockRoot, "started")
	continued := filepath.Join(lockRoot, "continued")
	cmd := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c",
		"touch '"+started+"'; sleep 5; touch '"+continued+"'")
	contractcheck.FailErr(t, "start protected command", cmd.Start())
	waitForFile(t, started, "protected command gate")

	contractcheck.FailErr(t, "terminate protected command", cmd.Process.Signal(syscall.SIGTERM))
	if err := cmd.Wait(); err == nil {
		t.Fatal("terminated lock command succeeded")
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(continued); !os.IsNotExist(err) {
		t.Fatalf("protected command continued after termination: %v", err)
	}

	writer := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c", "true")
	if out, err := writer.CombinedOutput(); err != nil {
		t.Fatalf("writer slot remained held after termination: %v\n%s", err, out)
	}
}

func TestSnapshotPublishHelperEpochDiscipline(t *testing.T) {
	lockRoot := t.TempDir()
	dest := filepath.Join(lockRoot, "generated.txt")

	snapshotPublish(t, lockRoot, dest, "one")
	if got := snapshotEpoch(t, lockRoot); got != "2" {
		t.Fatalf("first real publish left epoch %s, want 2", got)
	}
	snapshotPublish(t, lockRoot, dest, "one")
	if got := snapshotEpoch(t, lockRoot); got != "2" {
		t.Fatalf("identical publish moved epoch to %s; no-ops must not invalidate readers", got)
	}
	snapshotPublish(t, lockRoot, dest, "two")
	if got := snapshotEpoch(t, lockRoot); got != "4" {
		t.Fatalf("second real publish left epoch %s, want 4", got)
	}

	remove := snapshotCmd(t, lockRoot, "generate", "--", "bash", "-c",
		"source '"+snapshotPublishLib(t)+"'; snapshot_remove_file '"+dest+"'; snapshot_publish_finish")
	if out, err := remove.CombinedOutput(); err != nil {
		t.Fatalf("remove generated file: %v\n%s", err, out)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("generated file survived removal: %v", err)
	}
	if got := snapshotEpoch(t, lockRoot); got != "6" {
		t.Fatalf("removal left epoch %s, want 6", got)
	}
}
