//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"

	"golang.org/x/sys/unix"
)

type standardStreamReplacement struct {
	Event      string `json:"event"`
	Descriptor int    `json:"fd"`
	FileType   uint32 `json:"file_type"`
}

// Caller-supplied files must not become a native module source through a
// protected process's own /proc/self/fd paths. Pipes, sockets and character
// devices preserve terminal, desktop and journal transports.
func sanitizeStandardStreams() ([]standardStreamReplacement, error) {
	var streams [3]unix.Stat_t
	for descriptor := range streams {
		if err := unix.Fstat(descriptor, &streams[descriptor]); err != nil {
			return nil, err
		}
	}
	var replaced []standardStreamReplacement
	for descriptor, original := range streams {
		kind := original.Mode & unix.S_IFMT
		if kind == unix.S_IFIFO || kind == unix.S_IFSOCK || kind == unix.S_IFCHR {
			continue
		}
		access := unix.O_WRONLY
		if descriptor == 0 {
			access = unix.O_RDONLY
		}
		null, err := unix.Open("/dev/null", access|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return replaced, err
		}
		var actual unix.Stat_t
		err = unix.Fstat(null, &actual)
		if err == nil && (actual.Mode&unix.S_IFMT != unix.S_IFCHR || actual.Uid != 0 || actual.Rdev != unix.Mkdev(1, 3)) {
			err = errors.New("standard stream replacement is not the null device")
		}
		if err == nil {
			err = unix.Dup3(null, descriptor, 0)
		}
		_ = unix.Close(null)
		if err != nil {
			return replaced, err
		}
		replaced = append(replaced, standardStreamReplacement{Event: "protected_standard_stream_replaced", Descriptor: descriptor, FileType: kind})
	}
	return replaced, nil
}

// The bootstrap calls this only after its complete caller credential and
// capability drop, including on refusal. No privileged write reaches an
// inherited transport and diagnostics cannot change the launch verdict.
func reportStandardStreamReplacements(replaced []standardStreamReplacement) {
	for _, fact := range replaced {
		raw, err := json.Marshal(fact)
		if err == nil {
			_, _ = unix.Write(2, append(raw, '\n'))
		}
	}
}
