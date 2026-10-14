//go:build linux

package linuxsandbox

import (
	"os"
	"syscall"
	"testing"
)

func TestReadSignalReport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		written []byte
		want    syscall.Signal
	}{
		{"signal", []byte{byte(syscall.SIGSYS)}, syscall.SIGSYS},
		{"nothing written", nil, 0},
		{"zero", []byte{0}, 0},
		{"out of range", []byte{200}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			if len(tc.written) > 0 {
				if _, err := w.Write(tc.written); err != nil {
					t.Fatal(err)
				}
			}
			_ = w.Close()
			if got := readSignalReport(r); got != tc.want {
				t.Fatalf("readSignalReport = %v, want %v", got, tc.want)
			}
		})
	}
}
