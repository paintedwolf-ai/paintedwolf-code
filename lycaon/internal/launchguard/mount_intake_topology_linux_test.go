//go:build linux

package launchguard

import (
	"errors"
	"math"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestIntakeStableEpochStillRefusesMalformedTopology(t *testing.T) {
	// A regular read-only descriptor has no mount notification; the reader seam
	// supplies exact grammar counterexamples without changing the parser.
	file, err := os.CreateTemp(t.TempDir(), "stable-epoch")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	root := "20 1 8:1 / / rw - ext4 root rw\n"
	for _, raw := range []string{"", "invalid\n", root + root} {
		entries, changed, err := readIntakeMountTopology(int(file.Fd()), func(int, []byte) ([]byte, error) { return []byte(raw), nil })
		var refusal *mountTopologyRefusal
		if changed || len(entries) != 0 || !errors.As(err, &refusal) || MountIntakeExitStatus(err) < 100 {
			t.Fatalf("stable malformed input gained retry or admission: changed=%v entries=%v err=%v", changed, entries, err)
		}
	}
	entries, changed, err := readIntakeMountTopology(int(file.Fd()), func(int, []byte) ([]byte, error) { return []byte(root), nil })
	if err != nil || changed || len(entries) != 1 {
		t.Fatalf("stable valid topology refused: %v %v %v", entries, changed, err)
	}
}

func TestIntakeCaptureDescriptorAndReadErrorsRemainFatal(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "epoch-errors")
	if err != nil {
		t.Fatal(err)
	}
	fd := int(file.Fd())
	_, changed, err := readIntakeMountTopology(fd, func(int, []byte) ([]byte, error) { return nil, unix.EIO })
	if changed || !errors.Is(err, unix.EIO) || MountIntakeExitStatus(err) != 82 {
		t.Fatalf("read failure gained retry: %v %v", changed, err)
	}
	_, changed, err = readIntakeMountTopology(fd, func(int, []byte) ([]byte, error) {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return []byte("invalid\n"), nil
	})
	if changed || err == nil || MountIntakeExitStatus(err) != 80 {
		t.Fatalf("post-read invalid descriptor gained retry: %v %v", changed, err)
	}
	called := false
	_, changed, err = readIntakeMountTopology(fd, func(int, []byte) ([]byte, error) { called = true; return nil, nil })
	if called || changed || err == nil || MountIntakeExitStatus(err) != 80 {
		t.Fatalf("baseline invalid descriptor reached reader: %v %v %v", called, changed, err)
	}
	if changed, err := mountInfoEpochChanged(-1); changed || !errors.Is(err, unix.EBADF) {
		t.Fatalf("negative descriptor accepted: %v %v", changed, err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if changed, err := mountInfoEpochChanged(int(reader.Fd())); changed || err == nil {
		t.Fatalf("HUP became mount epoch change: %v %v", changed, err)
	}
}

func TestIntakePollRefusesDescriptorWidthBeforeReading(t *testing.T) {
	for _, fd := range []int{-1, math.MaxInt32 + 1} {
		called := false
		_, changed, err := readIntakeMountTopology(fd, func(int, []byte) ([]byte, error) {
			called = true
			return nil, nil
		})
		if called || changed || !errors.Is(err, unix.EBADF) || MountIntakeExitStatus(err) != 80 {
			t.Fatalf("unrepresentable descriptor reached topology read: fd=%d called=%v changed=%v err=%v", fd, called, changed, err)
		}
	}
}
