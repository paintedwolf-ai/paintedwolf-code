//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"golang.org/x/sys/unix"
)

type fuseProtocolDevice struct {
	requests [][]byte
	replies  [][]byte
	failure  error
	readErr  error
	short    bool
}

func (d *fuseProtocolDevice) Read(buffer []byte) (int, error) {
	if d.readErr != nil {
		err := d.readErr
		d.readErr = nil
		return 0, err
	}
	if len(d.requests) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, d.requests[0])
	d.requests = d.requests[1:]
	return n, nil
}

func (d *fuseProtocolDevice) Write(reply []byte) (int, error) {
	d.replies = append(d.replies, append([]byte(nil), reply...))
	if len(d.replies) == 1 && d.failure != nil {
		return 0, d.failure
	}
	if d.short {
		return len(reply) - 1, nil
	}
	return len(reply), nil
}

func TestSourceFUSEReplyPreservesInterruptedAndUnmountedRequests(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		failure error
		readErr error
		short   bool
		want    error
		replies int
	}{
		{name: "ordinary", replies: 2},
		{name: "interrupted", failure: unix.ENOENT, replies: 2},
		{name: "unmounted", failure: unix.ENODEV, replies: 1},
		{name: "aborted during reply", failure: unix.ECONNABORTED, replies: 1},
		{name: "aborted during read", readErr: unix.ECONNABORTED},
		{name: "device failure", failure: unix.EIO, want: unix.EIO, replies: 1},
		{name: "short write", short: true, want: io.ErrShortWrite, replies: 1},
		{name: "interrupted read", readErr: unix.EINTR, replies: 2},
		{name: "read device failure", readErr: unix.EIO, want: unix.EIO},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			device := &fuseProtocolDevice{failure: scenario.failure, readErr: scenario.readErr, short: scenario.short}
			for _, id := range []uint64{10, 11} {
				request := make([]byte, 40)
				binary.LittleEndian.PutUint32(request, uint32(len(request)))
				binary.LittleEndian.PutUint32(request[4:], 3) // GETATTR
				binary.LittleEndian.PutUint64(request[8:], id)
				binary.LittleEndian.PutUint64(request[16:], 1)
				device.requests = append(device.requests, request)
			}
			if err := serveSourceFUSE(device, 1000, 1000, nil); !errors.Is(err, scenario.want) {
				t.Fatalf("FUSE reply outcome: got %v, want %v", err, scenario.want)
			}
			if len(device.replies) != scenario.replies {
				t.Fatalf("reply attempts: got %d, want %d", len(device.replies), scenario.replies)
			}
			for i, reply := range device.replies {
				if binary.LittleEndian.Uint64(reply[8:]) != uint64(10+i) ||
					binary.LittleEndian.Uint32(reply) != uint32(len(reply)) {
					t.Fatalf("reply lost request identity or packet length: %x", reply)
				}
			}
		})
	}
}
