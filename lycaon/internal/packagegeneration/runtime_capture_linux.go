//go:build linux

package packagegeneration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type capturedRuntime struct {
	Sources []RuntimeSource
	Files   []RuntimeFile
	Aliases []RuntimeAlias
	Bytes   int64
}

// captureRuntime runs only inside the existing private generation candidate.
// Publication and lifetime selection remain with layout; this never executes a
// compiler, codec script, package manager or caller-selected program as root.
func (l layout) captureRuntime(origins []RuntimeOrigin, target *os.Root) (*capturedRuntime, error) {
	before, err := l.observeRuntime(origins)
	if err != nil {
		return nil, err
	}
	captured, err := l.copyRuntimeInputs(before, target)
	if err != nil {
		return nil, err
	}
	after, err := l.observeRuntime(origins)
	if err != nil {
		return nil, err
	}
	if !sameRuntimeSnapshot(before, after) {
		return nil, errors.New("runtime capture: selected input set changed; discard candidate and retry")
	}
	for _, origin := range origins {
		input := before[origin.SelectedRoot]
		if input.Kind == "symlink" {
			return nil, errors.New("runtime capture: projection root must be physical")
		}
		captured.Aliases = append(captured.Aliases, RuntimeAlias{Path: origin.SelectedRoot, Target: runtimeViewPath(origin.SelectedRoot), Kind: input.Kind})
	}
	return captured, nil
}

func runtimeViewPath(name string) string { return "view/" + strings.TrimPrefix(name, "/") }

func (l layout) copyRuntimeInputs(inputs runtimeSnapshot, target *os.Root) (*capturedRuntime, error) {
	if target == nil {
		return nil, errors.New("runtime capture: missing private candidate")
	}
	info, err := target.Stat(".")
	if err != nil {
		return nil, err
	}
	if err := controlledObject("runtime candidate", info, l.owner); err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("runtime capture: candidate must be private")
	}
	result := &capturedRuntime{}
	directories := map[string]bool{}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		item := inputs[name]
		relative := runtimeViewPath(name)
		for parent := filepath.Dir(relative); parent != "."; parent = filepath.Dir(parent) {
			directories[parent] = true
		}
		if item.Kind == "directory" {
			directories[relative] = true
		}
	}
	ordered := make([]string, 0, len(directories))
	for name := range directories {
		ordered = append(ordered, name)
	}
	slices.Sort(ordered)
	for _, name := range ordered {
		if err := target.Mkdir(name, 0700); err != nil {
			return nil, err
		}
		result.Files = append(result.Files, RuntimeFile{Path: name, Kind: "directory", Mode: 0555})
	}
	for _, name := range names {
		item := inputs[name]
		relative := runtimeViewPath(name)
		switch item.Kind {
		case "directory":
			continue
		case "symlink":
			destination := item.Target
			if filepath.IsAbs(destination) {
				destination = runtimeViewPath(destination)
			} else {
				destination = filepath.Clean(filepath.Join(filepath.Dir(relative), destination))
			}
			rewritten, err := filepath.Rel(filepath.Dir(relative), destination)
			if err != nil {
				return nil, err
			}
			if err := target.Symlink(rewritten, relative); err != nil {
				return nil, err
			}
			result.Files = append(result.Files, RuntimeFile{Path: relative, Kind: "symlink", Mode: 0777, Target: &rewritten})
		case "file":
			source, err := l.copyRuntimeFile(name, relative, item, target)
			if err != nil {
				return nil, err
			}
			result.Sources = append(result.Sources, source)
			digest, size := source.SHA256, source.Size
			result.Files = append(result.Files, RuntimeFile{Path: relative, Kind: "file", Mode: item.Facts.Mode & 0555, SHA256: &digest, Size: &size})
			result.Bytes += size
		default:
			return nil, errors.New("runtime capture: unknown selected kind")
		}
	}
	// Candidate directories stay private and writable for anchor preparation.
	// The publication transaction applies recorded final modes after binding.
	return result, nil
}

func (l layout) copyRuntimeFile(name, relative string, input runtimeInput, target *os.Root) (RuntimeSource, error) {
	source, err := os.Open(name)
	if err != nil {
		return RuntimeSource{}, err
	}
	defer func() { _ = source.Close() }()
	opened, err := source.Stat()
	if err != nil {
		return RuntimeSource{}, err
	}
	facts, err := runtimeFacts(opened)
	if err != nil {
		return RuntimeSource{}, err
	}
	if !opened.Mode().IsRegular() || facts != input.Facts {
		return RuntimeSource{}, errors.New("runtime capture: source changed while opening")
	}
	output, err := target.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return RuntimeSource{}, err
	}
	defer func() { _ = output.Close() }()
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(output, hasher), io.LimitReader(source, opened.Size()+1))
	if err != nil {
		return RuntimeSource{}, err
	}
	after, err := source.Stat()
	if err != nil {
		return RuntimeSource{}, err
	}
	afterFacts, err := runtimeFacts(after)
	if err != nil {
		return RuntimeSource{}, err
	}
	if size != opened.Size() || facts != afterFacts {
		return RuntimeSource{}, fmt.Errorf("runtime capture: source changed while copying %q", name)
	}
	if err := output.Chmod(os.FileMode(input.Facts.Mode & 0555)); err != nil {
		return RuntimeSource{}, err
	}
	if err := output.Sync(); err != nil {
		return RuntimeSource{}, err
	}
	return RuntimeSource{Path: name, CapturedPath: relative, SHA256: hex.EncodeToString(hasher.Sum(nil)), Size: size, Origin: input.Origin, Observation: &facts}, nil
}
