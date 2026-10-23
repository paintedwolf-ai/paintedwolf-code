//go:build linux

package packagegeneration

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

type runtimeResolvedDirectory struct {
	file *os.File
	path string
}
type runtimeResolvedObject struct {
	file        *os.File
	path        string
	directories []runtimeResolvedDirectory
}

type runtimeLinkWalk struct {
	layout   layout
	origin   RuntimeOrigin
	snapshot *runtimeSnapshot
	anchor   runtimeResolvedDirectory
	opened   []*os.File
	followed int
}

// Resolve raw path components in kernel order: a preceding symlink expands
// before '..'. Lexical cleaning of an unwalked target would select wrong bytes.
func (l layout) resolveRuntimeLink(name string, origin RuntimeOrigin, snapshot *runtimeSnapshot) (string, error) {
	anchor, err := l.runtimeSourceDirectory(l.anchor, snapshot)
	if err != nil {
		return "", err
	}
	if len(snapshot.roots)+1 > snapshot.handleLimit {
		_ = anchor.Close()
		return "", errors.New("runtime capture: insufficient link-walk descriptor headroom")
	}
	walk := runtimeLinkWalk{layout: l, origin: origin, snapshot: snapshot, anchor: runtimeResolvedDirectory{file: anchor, path: l.anchor}, opened: []*os.File{anchor}}
	defer func() {
		for _, file := range walk.opened {
			_ = file.Close()
		}
	}()
	parts, err := walk.absoluteParts(name)
	if err != nil {
		return "", err
	}
	selected, err := walk.resolve([]runtimeResolvedDirectory{walk.anchor}, parts)
	if err != nil {
		return "", err
	}
	if !walk.selected(selected.path) {
		return "", errors.New("runtime capture: link target leaves explicit selected root")
	}
	return selected.path, nil
}

func (w *runtimeLinkWalk) selected(name string) bool {
	return name == w.origin.SelectedRoot || strings.HasPrefix(name, w.origin.SelectedRoot+"/")
}

func (w *runtimeLinkWalk) absoluteParts(name string) ([]string, error) {
	if !filepath.IsAbs(name) {
		return nil, errors.New("runtime capture: expected absolute link path")
	}
	relative := ""
	if w.layout.anchor == "/" {
		relative = strings.TrimPrefix(name, "/")
	} else if name == w.layout.anchor {
		relative = ""
	} else if strings.HasPrefix(name, w.layout.anchor+"/") {
		relative = strings.TrimPrefix(name, w.layout.anchor+"/")
	} else {
		return nil, errors.New("runtime capture: link outside trust anchor")
	}
	return strings.Split(relative, "/"), nil
}

func (w *runtimeLinkWalk) resolve(stack []runtimeResolvedDirectory, parts []string) (runtimeResolvedObject, error) {
	stack = slices.Clone(stack)
	for index, component := range parts {
		switch component {
		case "", ".":
			continue
		case "..":
			if len(stack) == 1 {
				if w.layout.anchor != "/" {
					return runtimeResolvedObject{}, errors.New("runtime capture: link escapes trust anchor")
				}
			} else {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		parent := stack[len(stack)-1]
		child, err := openRuntimeAt(parent.file, component, unix.O_PATH)
		if err != nil {
			return runtimeResolvedObject{}, err
		}
		if len(w.opened)+len(w.snapshot.roots) >= w.snapshot.handleLimit {
			_ = child.Close()
			return runtimeResolvedObject{}, errors.New("runtime capture: link walk exceeds descriptor headroom")
		}
		w.opened = append(w.opened, child)
		physical := filepath.Join(parent.path, component)
		if len(w.snapshot.Custody) >= RuntimeSealMaxEntries {
			return runtimeResolvedObject{}, errors.New("runtime capture: link custody exceeds format ceiling")
		}
		if err := w.layout.recordRuntimeCustody(physical, child, w.snapshot); err != nil {
			return runtimeResolvedObject{}, err
		}
		info, err := child.Stat()
		if err != nil {
			return runtimeResolvedObject{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Linux pathname lookup follows at most forty links per resolution.
			w.followed++
			if w.followed > 40 {
				return runtimeResolvedObject{}, errors.New("runtime capture: cyclic or overlong kernel link chain")
			}
			target, err := runtimeReadlink(child)
			if err != nil {
				return runtimeResolvedObject{}, err
			}
			start, targetParts := stack, strings.Split(target, "/")
			if filepath.IsAbs(target) {
				start = []runtimeResolvedDirectory{w.anchor}
				targetParts, err = w.absoluteParts(target)
				if err != nil {
					return runtimeResolvedObject{}, err
				}
			}
			resolved, err := w.resolve(start, targetParts)
			if err != nil {
				return runtimeResolvedObject{}, err
			}
			if !w.selected(resolved.path) {
				return runtimeResolvedObject{}, errors.New("runtime capture: link referent leaves explicit selected root")
			}
			facts := w.snapshot.Custody[physical]
			link := RuntimeLink{Path: physical, Target: target, ResolvedPath: resolved.path, CapturedPath: runtimeViewPath(resolved.path), Origin: w.origin, Observation: &facts}
			if previous, exists := w.snapshot.Links[physical]; exists && !reflect.DeepEqual(previous, link) {
				return runtimeResolvedObject{}, errors.New("runtime capture: conflicting source link provenance")
			}
			if _, exists := w.snapshot.Links[physical]; !exists {
				if err := w.snapshot.observationRoom(1); err != nil {
					return runtimeResolvedObject{}, err
				}
			}
			w.snapshot.Links[physical] = link
			if index+1 == len(parts) {
				return resolved, nil
			}
			resolvedInfo, err := resolved.file.Stat()
			if err != nil {
				return runtimeResolvedObject{}, err
			}
			if !resolvedInfo.IsDir() {
				return runtimeResolvedObject{}, errors.New("runtime capture: source link crosses non-directory")
			}
			return w.resolve(resolved.directories, parts[index+1:])
		}
		if info.IsDir() {
			stack = append(stack, runtimeResolvedDirectory{file: child, path: physical})
			continue
		}
		if !info.Mode().IsRegular() || index+1 < len(parts) {
			return runtimeResolvedObject{}, errors.New("runtime capture: source link crosses unsupported object")
		}
		return runtimeResolvedObject{file: child, path: physical}, nil
	}
	directory := stack[len(stack)-1]
	return runtimeResolvedObject{file: directory.file, path: directory.path, directories: stack}, nil
}
