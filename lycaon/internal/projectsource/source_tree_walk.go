package projectsource

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"

	"github.com/lycaon/lycaon/internal/sourceeffect"
)

// walkSourceTree keeps the lexical order used by recovery manifests and tree
// fingerprints. Width and depth consume the operation's existing State account.
func walkSourceTree(ctx context.Context, root *os.Root, rel string, memory sourceeffect.Memory, visit func(string, fs.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame, err := memory.ReserveProgramState(ctx, 256+4*int64(len(rel)))
	if err != nil {
		return err
	}
	defer frame()
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if err := visit(rel, info); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	names, closeNames, err := sourceDirectoryNames(ctx, root, rel, memory)
	if err != nil {
		return err
	}
	defer closeNames()
	for _, name := range names {
		child, err := memory.ReserveProgramState(ctx, 256+4*int64(len(rel)+len(name)+1))
		if err != nil {
			return err
		}
		err = walkSourceTree(ctx, root, path.Join(rel, name), memory, visit)
		child()
		if err != nil {
			return err
		}
	}
	return nil
}

type sourceNameLease struct {
	release  func()
	previous *sourceNameLease
}

func sourceDirectoryNames(ctx context.Context, root *os.Root, rel string, memory sourceeffect.Memory) (names []string, closeNames func(), result error) {
	// Two pages cover the next ReadDir allocation while the preceding page is
	// still live. Retained names and replacement slice capacity are separate.
	page, err := memory.ReserveProgramState(ctx, 2*128*int64(2048+len(rel)))
	if err != nil {
		return nil, nil, err
	}
	var capacity func()
	var leases *sourceNameLease
	cleanup := func() {
		if capacity != nil {
			capacity()
		}
		for lease := leases; lease != nil; lease = lease.previous {
			lease.release()
		}
	}
	defer page()
	defer func() {
		if result != nil {
			cleanup()
		}
	}()
	dir, err := root.Open(rel)
	if err != nil {
		return nil, nil, err
	}
	defer func() { result = errors.Join(result, dir.Close()) }()
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		entries, readErr := dir.ReadDir(128)
		if len(entries) > 0 {
			retained := int64(64)
			for _, entry := range entries {
				retained += int64(len(entry.Name()))
			}
			release, err := memory.ReserveProgramState(ctx, retained)
			if err != nil {
				return nil, nil, err
			}
			leases = &sourceNameLease{release: release, previous: leases}
			needed := len(names) + len(entries)
			if needed > cap(names) {
				next := max(needed, 2*cap(names))
				release, err := memory.ReserveProgramState(ctx, 16*int64(next))
				if err != nil {
					return nil, nil, err
				}
				grown := make([]string, len(names), next)
				copy(grown, names)
				previous := capacity
				names = grown
				capacity = release
				if previous != nil {
					previous()
				}
			}
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	sortLease, err := memory.Reserve(ctx, 4<<10)
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(names)
	sortLease()
	return names, cleanup, nil
}
