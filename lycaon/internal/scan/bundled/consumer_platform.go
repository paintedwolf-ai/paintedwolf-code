package bundled

import (
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"path/filepath"
)

func inspectConsumerELF(path, goarch string) (ConsumerPlatform, error) {
	image, err := elf.Open(path)
	if err != nil {
		return ConsumerPlatform{}, fmt.Errorf("inspect final consumer ELF: %w", err)
	}
	defer func() { _ = image.Close() }()
	expected := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[goarch]
	if expected == elf.EM_NONE || image.Machine != expected || image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB {
		return ConsumerPlatform{}, fmt.Errorf("consumer ELF architecture differs from selected platform")
	}
	result := ConsumerPlatform{Machine: image.Machine.String()}
	for _, program := range image.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		if result.Interpreter != "" {
			return ConsumerPlatform{}, fmt.Errorf("consumer ELF repeats its interpreter")
		}
		raw, err := io.ReadAll(io.LimitReader(program.Open(), 4097))
		if err != nil || len(raw) > 4096 {
			return ConsumerPlatform{}, fmt.Errorf("consumer ELF interpreter exceeds its bound")
		}
		result.Interpreter = string(bytes.TrimRight(raw, "\x00"))
	}
	if !filepath.IsAbs(result.Interpreter) || filepath.Clean(result.Interpreter) != result.Interpreter {
		return ConsumerPlatform{}, fmt.Errorf("consumer ELF requires an explicit absolute interpreter")
	}
	result.Needed, err = image.DynString(elf.DT_NEEDED)
	if err != nil {
		return ConsumerPlatform{}, err
	}
	for _, tag := range []elf.DynTag{elf.DT_RPATH, elf.DT_RUNPATH} {
		values, err := image.DynString(tag)
		if err != nil {
			return ConsumerPlatform{}, err
		}
		result.RPaths = append(result.RPaths, values...)
	}
	return result, nil
}
