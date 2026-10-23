package packagegeneration

import (
	"debug/elf"
	"encoding/binary"
	"errors"
)

// PT_DYNAMIC's virtual address, not its independent file offset, selects the
// loader's table. Require those two views to describe exactly the same bytes.
func (image *runtimeModuleELF) readDynamicTable(program *elf.Prog) error {
	if program.Filesz == 0 || program.Filesz%16 != 0 || program.Memsz != program.Filesz ||
		program.Flags&elf.PF_R == 0 || !runtimeELFRange(image.raw, program.Off, program.Filesz) {
		return errors.New("module ELF: invalid dynamic table")
	}
	load, err := image.load(program.Vaddr, program.Filesz)
	if err != nil || load.Off+program.Vaddr-load.Vaddr != program.Off || program.Flags&^load.Flags != 0 {
		return errors.New("module ELF: dynamic table differs from loader-selected bytes")
	}
	for offset := program.Off; offset < program.Off+program.Filesz; offset += 16 {
		tag := elf.DynTag(binary.LittleEndian.Uint64(image.raw[offset:]))
		value := binary.LittleEndian.Uint64(image.raw[offset+8:])
		switch tag {
		case elf.DT_NULL:
			return nil
		case elf.DT_NEEDED, elf.DT_FILTER, elf.DT_AUXILIARY, elf.DT_AUDIT, elf.DT_DEPAUDIT:
			return errors.New("module ELF: external code dependency")
		}
		image.dynamic[tag] = append(image.dynamic[tag], value)
	}
	return errors.New("module ELF: unterminated dynamic table")
}
