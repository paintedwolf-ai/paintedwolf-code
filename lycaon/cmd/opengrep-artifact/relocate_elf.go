package main

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/scan/onefile"
)

type consumerELFRelocator struct {
	ctx     context.Context
	recipe  bundled.ConsumerRecipe
	loader  string
	roots   []string
	scratch string
	members map[string]onefile.Member
}

func (relocator consumerELFRelocator) relocate(name string, raw []byte) ([]byte, error) {
	if !bytes.HasPrefix(raw, []byte("\x7fELF")) {
		return raw, nil
	}
	if err := relocator.ctx.Err(); err != nil {
		return nil, err
	}
	image, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = image.Close() }()
	expected := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]
	if image.Machine != expected || image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB {
		return nil, fmt.Errorf("embedded ELF architecture differs: %s", name)
	}
	interpreter, dynamic := false, false
	for _, program := range image.Progs {
		interpreter = interpreter || program.Type == elf.PT_INTERP
		dynamic = dynamic || program.Type == elf.PT_DYNAMIC
	}
	if !dynamic {
		return raw, nil
	}
	var search []string
	for _, tag := range []elf.DynTag{elf.DT_RPATH, elf.DT_RUNPATH} {
		values, err := image.DynString(tag)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			for _, entry := range strings.Split(value, ":") {
				if entry != "$ORIGIN" && !strings.HasPrefix(entry, "$ORIGIN/") {
					return nil, fmt.Errorf("embedded ELF has an uncontrolled original RPATH: %s", name)
				}
				inside := path.Join(path.Dir(name), strings.TrimPrefix(strings.TrimPrefix(entry, "$ORIGIN"), "/"))
				if inside == ".." || strings.HasPrefix(inside, "../") {
					return nil, fmt.Errorf("embedded ELF RPATH escapes its payload: %s", name)
				}
				search = append(search, entry)
			}
		}
	}
	search = append(search, relocator.roots...)
	needed, err := image.DynString(elf.DT_NEEDED)
	if err != nil {
		return nil, err
	}
	for _, dependency := range needed {
		if !relocator.dependencyPresent(name, dependency, search) {
			return nil, fmt.Errorf("embedded ELF dependency has no declared payload/store provider: %s: %s", name, dependency)
		}
	}
	file, err := os.CreateTemp(relocator.scratch, "elf-")
	if err != nil {
		return nil, err
	}
	fileName := file.Name()
	defer func() { _ = os.Remove(fileName) }()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	arguments := []string{"--set-rpath", strings.Join(search, ":")}
	if interpreter {
		arguments = append(arguments, "--set-interpreter", relocator.loader)
	}
	arguments = append(arguments, fileName)
	// #nosec G204 -- build-only tool pinned by the closed consumer recipe; arguments are constructed here.
	command := exec.CommandContext(relocator.ctx, relocator.recipe.ToolPath, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("relocate ELF %s: %w: %.4096s", name, err, output)
	}
	return readConsumerInput(fileName, onefile.MaxBytes)
}

func (relocator consumerELFRelocator) dependencyPresent(name, dependency string, search []string) bool {
	if filepath.Base(dependency) != dependency || dependency == "." || dependency == ".." {
		return false
	}
	for _, directory := range search {
		if strings.HasPrefix(directory, "$ORIGIN") {
			relative := path.Join(path.Dir(name), strings.TrimPrefix(directory, "$ORIGIN"), dependency)
			for range 32 {
				member, ok := relocator.members[relative]
				if !ok {
					break
				}
				if member.Link == "" {
					if bytes.HasPrefix(member.Data, []byte("\x7fELF")) {
						return true
					}
					break
				}
				relative = path.Join(path.Dir(relative), member.Link)
			}
		} else {
			resolved, err := filepath.EvalSymlinks(filepath.Join(directory, dependency))
			if err != nil || !strings.HasPrefix(resolved, "/nix/store/") {
				continue
			}
			info, err := os.Stat(resolved)
			if err == nil && info.Mode().IsRegular() {
				return true
			}
		}
	}
	return false
}
