package packagegeneration

import (
	"debug/elf"
	"encoding/binary"
	"errors"
)

// Dynamic relocation pointers, rather than section names, select what rtld runs.
// Checking both prevents a forged section table from concealing an IRELATIVE.
func (image *runtimeModuleELF) checkDynamicRelocations() error {
	for _, group := range []struct {
		address, size, entry elf.DynTag
		width                uint64
	}{
		{elf.DT_REL, elf.DT_RELSZ, elf.DT_RELENT, 16},
		{elf.DT_RELA, elf.DT_RELASZ, elf.DT_RELAENT, 24},
	} {
		address, hasAddress, err := image.singleTag(group.address)
		if err != nil {
			return err
		}
		size, hasSize, err := image.singleTag(group.size)
		if err != nil {
			return err
		}
		entry, hasEntry, err := image.singleTag(group.entry)
		if err != nil {
			return err
		}
		if !hasAddress && !hasSize && !hasEntry {
			continue
		}
		if !hasAddress || !hasSize || !hasEntry || entry != group.width {
			return errors.New("module ELF: incomplete dynamic relocations")
		}
		if err := image.checkRelocationBytes(address, size, group.width); err != nil {
			return err
		}
	}
	address, hasAddress, err := image.singleTag(elf.DT_JMPREL)
	if err != nil {
		return err
	}
	size, hasSize, err := image.singleTag(elf.DT_PLTRELSZ)
	if err != nil {
		return err
	}
	kind, hasKind, err := image.singleTag(elf.DT_PLTREL)
	if err != nil {
		return err
	}
	if !hasAddress && !hasSize && !hasKind {
		return nil
	}
	if !hasAddress || !hasSize || !hasKind || elf.DynTag(kind) != elf.DT_REL && elf.DynTag(kind) != elf.DT_RELA {
		return errors.New("module ELF: incomplete PLT relocations")
	}
	width := uint64(16)
	if elf.DynTag(kind) == elf.DT_RELA {
		width = 24
	}
	return image.checkRelocationBytes(address, size, width)
}

func (image *runtimeModuleELF) singleTag(tag elf.DynTag) (uint64, bool, error) {
	values := image.dynamic[tag]
	if len(values) > 1 {
		return 0, false, errors.New("module ELF: duplicate dynamic tag")
	}
	if len(values) == 0 {
		return 0, false, nil
	}
	return values[0], true, nil
}

func (image *runtimeModuleELF) checkRelocationBytes(address, size, width uint64) error {
	if size%width != 0 {
		return errors.New("module ELF: malformed dynamic relocation size")
	}
	if size == 0 {
		return nil
	}
	program, err := image.load(address, size)
	if err != nil {
		return err
	}
	start := program.Off + address - program.Vaddr
	for offset := uint64(0); offset < size; offset += width {
		kind := uint32(binary.LittleEndian.Uint64(image.raw[start+offset+8:]))
		if image.runtimeIFUNCRelocation(kind) {
			return errors.New("module ELF: dynamic IFUNC relocation")
		}
	}
	return nil
}

func (image *runtimeModuleELF) runtimeIFUNCRelocation(kind uint32) bool {
	return image.architecture == "x86_64" && kind == uint32(elf.R_X86_64_IRELATIVE) ||
		image.architecture == "aarch64" && kind == uint32(elf.R_AARCH64_IRELATIVE)
}
