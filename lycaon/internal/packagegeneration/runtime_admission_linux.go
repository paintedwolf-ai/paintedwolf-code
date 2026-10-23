//go:build linux

package packagegeneration

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Capture needs temporary descriptors for a child probe/directory reader and
// the streaming source/output pair. It never changes the process or host limit.
const runtimeTemporaryDescriptors = 4

func runtimeHandleBudget(limit uint64, baseline int) (int, error) {
	maximum := uint64(RuntimeSealMaxEntries + baseline + runtimeTemporaryDescriptors)
	remaining := int(min(limit, maximum)) - baseline - runtimeTemporaryDescriptors
	if remaining < 1 {
		return 0, errors.New("runtime capture: insufficient descriptor headroom")
	}
	return remaining, nil
}

func runtimeDescriptorAdmission() (int, error) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return 0, err
	}
	directory, err := os.Open("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	defer func() { _ = directory.Close() }()
	baseline := 0
	for {
		entries, err := directory.ReadDir(128)
		baseline += len(entries)
		if baseline > RuntimeSealMaxEntries {
			return 0, errors.New("runtime capture: descriptor baseline exceeds format ceiling")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	return runtimeHandleBudget(limit.Cur, baseline)
}

func (s *runtimeSnapshot) retainDirectory(file *os.File) error {
	if len(s.roots) >= s.handleLimit {
		_ = file.Close()
		return errors.New("runtime capture: retained directories exceed descriptor headroom")
	}
	s.roots = append(s.roots, file)
	return nil
}

func (s *runtimeSnapshot) observationRoom(extra int) error {
	if len(s.Inputs)+len(s.Custody)+len(s.Links)+extra > RuntimeSealMaxEntries {
		return errors.New("runtime capture: combined observations exceed format ceiling")
	}
	return nil
}

// Count final captured rows before copying. Added anchors and the inventory are
// checked again by the complete final seal before any publication is possible.
func admitRuntimeRows(snapshot *runtimeSnapshot, aliases int) (int64, error) {
	count := len(snapshot.Links) + aliases
	files := map[string]bool{}
	var total int64
	for name, item := range snapshot.Inputs {
		if item.Kind == "file" {
			if item.Size < 0 || item.Size > math.MaxInt64-total {
				return 0, errors.New("runtime capture: selected byte count overflows")
			}
			total += item.Size
			count++ // original source provenance row
		}
		for relative := runtimeViewPath(name); relative != "."; relative = filepath.Dir(relative) {
			if files[relative] {
				continue
			}
			count++
			if count > RuntimeSealMaxEntries {
				return 0, errors.New("runtime capture: combined retained rows exceed format ceiling")
			}
			files[relative] = true
		}
	}
	if count > RuntimeSealMaxEntries {
		return 0, errors.New("runtime capture: combined retained rows exceed format ceiling")
	}
	return total, nil
}

func admitRuntimeSpace(target *os.Root, bytes int64) error {
	directory, err := target.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	var capacity unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &capacity); err != nil {
		return err
	}
	if capacity.Bsize <= 0 {
		return errors.New("runtime capture: filesystem reports invalid capacity")
	}
	available := uint64(math.MaxInt64)
	if capacity.Bavail <= available/uint64(capacity.Bsize) {
		available = capacity.Bavail * uint64(capacity.Bsize)
	}
	if bytes < 0 || uint64(bytes) > available {
		return errors.New("runtime capture: selected bytes exceed available candidate space")
	}
	return nil
}
