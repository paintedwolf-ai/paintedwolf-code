//go:build linux

package confine_test

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const streamProbeMode = "PW_STANDARD_STREAM_PROBE"

func TestLinuxStandardStreamProbeProcess(t *testing.T) {
	mode := os.Getenv(streamProbeMode)
	if mode == "" {
		return
	}
	switch mode {
	case "output":
		for _, step := range []struct {
			fd   int
			text string
		}{{1, "one"}, {2, "two"}, {1, "three"}, {2, "four"}} {
			if _, err := unix.Write(step.fd, []byte(step.text)); err != nil {
				os.Exit(31)
			}
		}
		for _, fd := range []int{1, 2} {
			_ = unix.Fchmod(fd, 0o777)
			_ = unix.Fchown(fd, -1, os.Getgid())
			_ = unix.Futimes(fd, []unix.Timeval{{Sec: 123456789}, {Sec: 123456789}})
			_ = unix.Fsetxattr(fd, "user.stream-fixture", []byte("changed"), 0)
		}
	case "early":
	case "pipe":
		var st unix.Stat_t
		if unix.Fstat(0, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
			os.Exit(32)
		}
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(33)
		}
	case "terminal":
		if _, err := unix.IoctlGetTermios(0, unix.TCGETS); err != nil {
			os.Exit(34)
		}
	case "cancel":
		self, err := os.Executable()
		if err != nil {
			os.Exit(35)
		}
		child := exec.Command(self, "-test.run=^TestLinuxStandardStreamProbeProcess$")
		child.Env = append(os.Environ(), streamProbeMode+"=hold")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(36)
		}
		_, _ = os.Stdout.WriteString("copy-ready\n")
		holdStreamProbe()
	case "hold":
		holdStreamProbe()
	default:
		os.Exit(37)
	}
	os.Exit(0)
}

func holdStreamProbe() {
	read, write, err := os.Pipe()
	if err != nil {
		os.Exit(38)
	}
	defer write.Close()
	defer read.Close()
	_, _ = read.Read(make([]byte, 1))
}

func standardStreamCommand(t *testing.T, ctx context.Context, c confine.Confinement, mode string) (*exec.Cmd, func()) {
	t.Helper()
	self := requireLinuxSandbox(t)
	cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{"-test.run=^TestLinuxStandardStreamProbeProcess$"}, c)
	testutil.FailErr(t, "prepare standard stream command", err)
	cmd.Env = append(os.Environ(), streamProbeMode+"="+mode)
	return cmd, cleanup
}

func TestLinuxRegularOutputsCarryOrderedBytesWithoutMetadataAuthority(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output-")
	testutil.FailErr(t, "create owned regular output", err)
	defer output.Close()
	testutil.FailErr(t, "set output fixture mode", output.Chmod(0o640))
	testutil.FailErr(t, "seed output fixture xattr", unix.Fsetxattr(int(output.Fd()), "user.stream-fixture", []byte("original"), 0))
	var before unix.Stat_t
	testutil.FailErr(t, "inspect original output metadata", unix.Fstat(int(output.Fd()), &before))
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd, cleanup := standardStreamCommand(t, ctx, denyConfinement(t.TempDir()), "output")
	defer cleanup()
	cmd.Stdout, cmd.Stderr = output, output
	testutil.FailErr(t, "deliver ordered regular outputs", cmd.Run())
	data, err := os.ReadFile(output.Name())
	testutil.FailErr(t, "read regular output bytes", err)
	if string(data) != "onetwothreefour" {
		t.Fatalf("ordered output=%q", data)
	}
	var after unix.Stat_t
	testutil.FailErr(t, "inspect regular output metadata", unix.Fstat(int(output.Fd()), &after))
	if before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid || after.Mtim.Sec == 123456789 {
		t.Fatalf("target changed output metadata: before=%+v after=%+v", before, after)
	}
	buf := make([]byte, 16)
	n, err := unix.Fgetxattr(int(output.Fd()), "user.stream-fixture", buf)
	testutil.FailErr(t, "read original output xattr", err)
	if string(buf[:n]) != "original" {
		t.Fatal("target changed output attribute")
	}
}

func TestLinuxByteInputEarlyExitAndCancellationSettleDescendants(t *testing.T) {
	for _, mode := range []string{"early", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			input, err := os.CreateTemp(t.TempDir(), "input-")
			testutil.FailErr(t, "create bounded input fixture", err)
			defer input.Close()
			testutil.FailErr(t, "size bounded input fixture", input.Truncate(64<<20))
			c := denyConfinement(t.TempDir())
			lease, err := confine.BindAction(&c, confine.EgressCommand{ToolCallID: "byte-stream-" + mode})
			testutil.FailErr(t, "bind stream descendants", err)
			defer lease.Close(context.Background())
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			cmd, cleanup := standardStreamCommand(t, ctx, c, mode)
			defer cleanup()
			cmd.Stdin = input
			if mode == "early" {
				testutil.FailErr(t, "settle early-exiting byte input", cmd.Run())
			} else {
				output, err := cmd.StdoutPipe()
				testutil.FailErr(t, "open cancellation readiness pipe", err)
				testutil.FailErr(t, "start cancellable byte input", cmd.Start())
				line, err := bufio.NewReader(output).ReadString('\n')
				testutil.FailErr(t, "read actual descendant readiness", err)
				if line != "copy-ready\n" {
					t.Fatalf("unexpected cancellation readiness %q", line)
				}
				cancel()
				if err := cmd.Wait(); err == nil {
					t.Fatal("cancelled command reported success")
				}
			}
			offset, err := input.Seek(0, io.SeekCurrent)
			testutil.FailErr(t, "inspect bounded input consumption", err)
			if offset >= 64<<20 {
				t.Fatal("early exit/cancellation consumed the whole input")
			}
			// Wait joins the outer helper; its PID namespace may still be exiting.
			deadline := time.Now().Add(5 * time.Second)
			for {
				survivors, err := lease.Survivors()
				testutil.FailErr(t, "inspect stream descendants after settlement", err)
				if len(survivors) == 0 {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("stream descendants survived: %+v", survivors)
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
}

func TestLinuxNonregularInputRetainsPipeAndTerminalSemantics(t *testing.T) {
	t.Run("pipe", func(t *testing.T) {
		read, write, err := os.Pipe()
		testutil.FailErr(t, "create input pipe", err)
		defer read.Close()
		done := make(chan error, 1)
		go func() { _, err := write.WriteString("pipe bytes"); _ = write.Close(); done <- err }()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		cmd, cleanup := standardStreamCommand(t, ctx, denyConfinement(t.TempDir()), "pipe")
		defer cleanup()
		cmd.Stdin = read
		output, err := cmd.CombinedOutput()
		testutil.FailErr(t, "execute pipe input", err)
		testutil.FailErr(t, "finish pipe writer", <-done)
		if string(output) != "pipe bytes" {
			t.Fatalf("pipe output=%q", output)
		}
	})
	t.Run("terminal", func(t *testing.T) {
		master, slave := openPty(t)
		defer master.Close()
		defer slave.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		cmd, cleanup := standardStreamCommand(t, ctx, denyConfinement(t.TempDir()), "terminal")
		defer cleanup()
		cmd.Stdin = slave
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("terminal input lost its ABI: %v: %s", err, out)
		}
	})
}

func TestLinuxClosedInputArrivesAsNullStream(t *testing.T) {
	requireLinuxSandbox(t)
	file, err := os.CreateTemp(t.TempDir(), "closed-")
	testutil.FailErr(t, "create closed descriptor fixture", err)
	testutil.FailErr(t, "close descriptor fixture", file.Close())
	outside := filepath.Join(outsideTemporaryWriteRoots(t), "outside")
	seedMetadataFile(t, outside)
	script := []string{"stdin-null input", "fchmod-stdin input", "fchown-stdin input", "futimes-stdin input", "fsetxattr-stdin input", "chmod " + outside}
	got := runMetadata(t, denyConfinement(t.TempDir()), script, file)
	if got[script[0]] != "ok" {
		t.Fatalf("closed input did not arrive as an empty character stream: %v", got)
	}
	for _, line := range script[1:5] {
		if _, ok := got[line]; !ok {
			t.Fatalf("descriptor operation did not complete: %s", line)
		}
	}
	if got[script[5]] != "EROFS" {
		t.Fatalf("outside path metadata = %q, want EROFS", got[script[5]])
	}
}
