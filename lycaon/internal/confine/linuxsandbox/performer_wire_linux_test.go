//go:build linux

package linuxsandbox

import (
	"bytes"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPathCallFramesRoundTrip(t *testing.T) {
	want := pathCall{Op: callRename, Absolute: 2, Dirs: 2, Mode: 0o644, Umask: 0o22, Flags: 1 << 40,
		Dev: 7, Resolve: 3, Notification: 1<<63 + 5, Caller: 4242, Path: "a", NewPath: "b/c", Content: ""}
	raw, err := encodePathCall(want)
	testutil.FailErr(t, "encodePathCall failed", err)
	got, err := decodePathCall(raw)
	testutil.FailErr(t, "decodePathCall failed", err)
	if got != want {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
}

func TestPathCallFramesRefuseMalformedInput(t *testing.T) {
	if _, err := encodePathCall(pathCall{Op: "chmod"}); err == nil {
		t.Fatal("encoded an unknown op")
	}
	if _, err := encodePathCall(pathCall{Op: callOpen, Dirs: 3}); err == nil {
		t.Fatal("encoded three directories")
	}
	raw, err := encodePathCall(pathCall{Op: callSymlink, Dirs: 1, Notification: 9, Path: "name", Content: "target"})
	testutil.FailErr(t, "encodePathCall failed", err)
	bad := map[string][]byte{
		"short":     raw[:pathCallHeader-1],
		"truncated": raw[:len(raw)-1],
		"trailing":  append(append([]byte{}, raw...), 0),
	}
	for _, mutate := range []struct {
		name string
		at   int
		b    byte
	}{{"op zero", 0, 0}, {"op unknown", 0, byte(len(callOps))}, {"dirs", 2, 3}, {"reserved", 3, 1}, {"caller", 51, 0xff}} {
		b := append([]byte{}, raw...)
		b[mutate.at] = mutate.b
		bad[mutate.name] = b
	}
	for name, b := range bad {
		if call, err := decodePathCall(b); err == nil || call != (pathCall{}) {
			t.Errorf("%s: decoded a malformed frame as %+v", name, call)
		}
	}
}

// A plan of any length arrives whole, since a packet longer than the reader's
// buffer would be cut without an error.
func TestFloorsArriveWholeAtAnySize(t *testing.T) {
	for _, size := range []int{1, maxPathMessage - 1, maxPathMessage, maxPathMessage + 1, 40 * maxPathMessage} {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("socketpair: %v", err)
		}
		want := bytes.Repeat([]byte("f"), size)
		sent := make(chan error, 1)
		go func() { sent <- sendFloors(pair[0], want) }()
		got, err := receiveFloors(pair[1], make([]byte, maxPathMessage))
		if err != nil {
			t.Fatalf("size %d: receive: %v", size, err)
		}
		if err := <-sent; err != nil {
			t.Fatalf("size %d: send: %v", size, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("size %d: got %d bytes", size, len(got))
		}
		_ = unix.Close(pair[0])
		_ = unix.Close(pair[1])
	}
}
