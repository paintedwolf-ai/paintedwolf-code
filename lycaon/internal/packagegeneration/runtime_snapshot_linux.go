//go:build linux

package packagegeneration

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"
)

// Compare the entire selected set before and after copying. This is a bounded
// package-update consistency check, not an atomic host filesystem snapshot.
type runtimeSnapshot struct {
	Inputs      map[string]runtimeInput
	Custody     map[string]KernelObservation
	Links       map[string]RuntimeLink
	roots       []*os.File
	handleLimit int
}

type runtimeInput struct {
	Origin                 RuntimeOrigin
	Facts                  KernelObservation
	Size                   int64
	Kind, Target, Resolved string
	Children               []string
	root                   *os.File
	relative               string
}

func (s *runtimeSnapshot) Close() {
	for _, root := range s.roots {
		_ = root.Close()
	}
	s.roots = nil
}

func runtimeFacts(info os.FileInfo) (KernelObservation, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return KernelObservation{}, errors.New("runtime capture: missing kernel facts")
	}
	return KernelObservation{Device: uint64(stat.Dev), Inode: stat.Ino, MtimeNS: stat.Mtim.Nano(), CtimeNS: stat.Ctim.Nano(), UID: stat.Uid, GID: stat.Gid, Mode: stat.Mode}, nil
}

func (l layout) observeRuntime(origins []RuntimeOrigin) (*runtimeSnapshot, error) {
	if len(origins) > RuntimeSealMaxEntries {
		return nil, errors.New("runtime capture: too many selected roots")
	}
	handleLimit, err := runtimeDescriptorAdmission()
	if err != nil {
		return nil, err
	}
	snapshot := &runtimeSnapshot{handleLimit: handleLimit, Inputs: map[string]runtimeInput{}, Custody: map[string]KernelObservation{}, Links: map[string]RuntimeLink{}}
	complete := false
	defer func() {
		if !complete {
			snapshot.Close()
		}
	}()
	seen := map[string]bool{}
	for _, origin := range origins {
		if seen[origin.SelectedRoot] {
			return nil, errors.New("runtime capture: duplicate selected root")
		}
		seen[origin.SelectedRoot] = true
	}
	for _, origin := range origins {
		// Nix requires explicit store-root custody and immutable-output proof, never
		// a reinterpretation of generic provenance as native root authority.
		if origin.Kind != "native" || origin.StoreRoot != "" || origin.OutputPath != "" || !runtimeAbsolute(origin.SelectedRoot) {
			return nil, errors.New("runtime capture: unsupported native source authority")
		}
		parent, err := l.runtimeSourceDirectory(filepath.Dir(origin.SelectedRoot), snapshot)
		if err != nil {
			return nil, err
		}
		if err := snapshot.retainDirectory(parent); err != nil {
			return nil, err
		}
		if err := l.observeRuntimePath(origin.SelectedRoot, filepath.Base(origin.SelectedRoot), parent, origin, snapshot); err != nil {
			return nil, err
		}
	}
	if len(snapshot.Inputs) == 0 {
		return nil, errors.New("runtime capture: empty input selection")
	}
	for _, link := range snapshot.Links {
		target, exists := snapshot.Inputs[link.ResolvedPath]
		if !exists || (target.Kind != "file" && target.Kind != "directory") {
			return nil, errors.New("runtime capture: resolved link target is absent from explicit retained set")
		}
	}
	complete = true
	return snapshot, nil
}

func (l layout) observeRuntimePath(name, relative string, root *os.File, origin RuntimeOrigin, snapshot *runtimeSnapshot) error {
	if !runtimeAbsolute(name) {
		return errors.New("runtime capture: invalid selected path")
	}
	if previous, exists := snapshot.Inputs[name]; exists {
		if previous.Origin != origin {
			return fmt.Errorf("runtime capture: overlapping source authorities %q", name)
		}
		return nil
	}
	if len(snapshot.Inputs) >= RuntimeSealMaxEntries {
		return errors.New("runtime capture: input set exceeds format ceiling")
	}
	selected, err := openRuntimeAt(root, relative, unix.O_PATH)
	if err != nil {
		return err
	}
	defer func() { _ = selected.Close() }()
	if err := l.recordRuntimeCustody(name, selected, snapshot); err != nil {
		return err
	}
	info, err := selected.Stat()
	if err != nil {
		return err
	}
	facts, err := runtimeFacts(info)
	if err != nil {
		return err
	}
	item := runtimeInput{Origin: origin, Facts: facts, Size: info.Size(), root: root, relative: relative}
	switch {
	case info.IsDir():
		item.Kind = "directory"
		directory, err := openRuntimeAt(root, relative, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			return err
		}
		opened, err := directory.Stat()
		if err != nil {
			_ = directory.Close()
			return err
		}
		if !os.SameFile(info, opened) {
			_ = directory.Close()
			return errors.New("runtime capture: selected directory changed while opening")
		}
		_ = selected.Close() // The retained readable directory now carries custody.
		// Retaining each opened directory binds every child to the validated walk.
		if err := snapshot.retainDirectory(directory); err != nil {
			return err
		}
		children, err := readRuntimeChildren(directory, RuntimeSealMaxEntries-len(snapshot.Inputs)-1)
		if err != nil {
			return err
		}
		item.Children = children
		if err := snapshot.observationRoom(1); err != nil {
			return err
		}
		snapshot.Inputs[name] = item
		for _, child := range children {
			if err := l.observeRuntimePath(filepath.Join(name, child), child, directory, origin, snapshot); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		item.Kind = "file"
	case info.Mode()&os.ModeSymlink != 0:
		if name == origin.SelectedRoot {
			return errors.New("runtime capture: projection root must be physical")
		}
		item.Kind = "symlink"
		item.Target, err = runtimeReadlink(selected)
		if err != nil {
			return err
		}
		item.Resolved, err = l.resolveRuntimeLink(name, origin, snapshot)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("runtime capture: unsupported source object %q", name)
	}
	if err := snapshot.observationRoom(1); err != nil {
		return err
	}
	snapshot.Inputs[name] = item
	return nil
}

func readRuntimeChildren(directory *os.File, remaining int) ([]string, error) {
	var names []string
	for {
		count := min(128, remaining-len(names)+1)
		if count < 1 {
			return nil, errors.New("runtime capture: directory exceeds remaining entry ceiling")
		}
		entries, err := directory.ReadDir(count)
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) > remaining {
			return nil, errors.New("runtime capture: directory exceeds remaining entry ceiling")
		}
		if errors.Is(err, io.EOF) {
			slices.Sort(names)
			return names, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func sameRuntimeSnapshot(before, after *runtimeSnapshot) bool {
	if !reflect.DeepEqual(before.Custody, after.Custody) || !reflect.DeepEqual(before.Links, after.Links) || len(before.Inputs) != len(after.Inputs) {
		return false
	}
	for name, input := range before.Inputs {
		other, exists := after.Inputs[name]
		if !exists {
			return false
		}
		input.root = nil
		other.root = nil
		if !reflect.DeepEqual(input, other) {
			return false
		}
	}
	return true
}
