//go:build linux

package packagegeneration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"
)

// A snapshot is a bounded observation of the entire selected input set. It is
// compared again after copying; individual file stability alone would accept a
// mixture observed during a package transaction. This is not an atomic host
// filesystem snapshot and never makes package-manager text authoritative.
type runtimeSnapshot map[string]runtimeInput

type runtimeInput struct {
	Origin   RuntimeOrigin
	Facts    KernelObservation
	Kind     string
	Target   string
	Children []string
}

func runtimeFacts(info os.FileInfo) (KernelObservation, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return KernelObservation{}, errors.New("runtime capture: missing kernel facts")
	}
	return KernelObservation{Device: uint64(stat.Dev), Inode: stat.Ino, MtimeNS: stat.Mtim.Nano(), CtimeNS: stat.Ctim.Nano(), UID: stat.Uid, GID: stat.Gid, Mode: stat.Mode}, nil
}

func (l layout) observeRuntime(origins []RuntimeOrigin) (runtimeSnapshot, error) {
	observed := runtimeSnapshot{}
	for _, origin := range origins {
		// The native producer has strict custody. Nix outputs require their own
		// explicit store-root exception and immutable-output proof, never this path.
		if origin.Kind != "native" || origin.StoreRoot != "" || origin.OutputPath != "" || !runtimeAbsolute(origin.SelectedRoot) {
			return nil, errors.New("runtime capture: unsupported native source authority")
		}
		if err := l.controlledParents(filepath.Dir(origin.SelectedRoot)); err != nil {
			return nil, err
		}
		if err := l.observeRuntimePath(origin.SelectedRoot, origin, observed); err != nil {
			return nil, err
		}
	}
	if len(observed) == 0 {
		return nil, errors.New("runtime capture: empty input selection")
	}
	return observed, nil
}

func (l layout) observeRuntimePath(name string, origin RuntimeOrigin, observed runtimeSnapshot) error {
	if previous, exists := observed[name]; exists {
		if !reflect.DeepEqual(previous.Origin, origin) {
			return fmt.Errorf("runtime capture: overlapping source authorities %q", name)
		}
		return nil
	}
	if len(observed) >= RuntimeSealMaxEntries {
		return errors.New("runtime capture: input set exceeds format ceiling")
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if err := controlledObject(name, info, l.owner); err != nil {
		return err
	}
	facts, err := runtimeFacts(info)
	if err != nil {
		return err
	}
	item := runtimeInput{Origin: origin, Facts: facts}
	switch {
	case info.IsDir():
		item.Kind = "directory"
		directory, err := os.Open(name)
		if err != nil {
			return err
		}
		opened, statErr := directory.Stat()
		children, readErr := directory.ReadDir(-1)
		closeErr := directory.Close()
		if err := errors.Join(statErr, readErr, closeErr); err != nil {
			return err
		}
		if !os.SameFile(info, opened) {
			return errors.New("runtime capture: source directory changed while opening")
		}
		for _, child := range children {
			item.Children = append(item.Children, child.Name())
		}
		slices.Sort(item.Children)
	case info.Mode().IsRegular():
		item.Kind = "file"
	case info.Mode()&os.ModeSymlink != 0:
		item.Kind = "symlink"
		item.Target, err = os.Readlink(name)
		if err != nil {
			return err
		}
		// Every link must resolve inside a selected root. The final seal validates
		// retained link graphs, so a captured module cannot fall back to live bytes.
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(origin.SelectedRoot, resolved)
		if err != nil || relative == ".." || len(relative) > 3 && relative[:3] == "../" {
			return fmt.Errorf("runtime capture: link leaves selected source %q", name)
		}
	default:
		return fmt.Errorf("runtime capture: unsupported source object %q", name)
	}
	observed[name] = item
	for _, child := range item.Children {
		if err := l.observeRuntimePath(filepath.Join(name, child), origin, observed); err != nil {
			return err
		}
	}
	return nil
}

func sameRuntimeSnapshot(before, after runtimeSnapshot) bool { return reflect.DeepEqual(before, after) }
