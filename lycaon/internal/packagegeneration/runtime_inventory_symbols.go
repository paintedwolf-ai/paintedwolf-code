package packagegeneration

import (
	"debug/elf"
	"errors"
)

// Bind the section view used by debug/elf to rtld's actual dynamic pointers.
func (image *runtimeModuleELF) checkSymbolTables() error {
	for _, section := range image.file.Sections {
		if section.Type != elf.SHT_SYMTAB && section.Type != elf.SHT_DYNSYM {
			continue
		}
		if section.Entsize != 24 || section.Size%24 != 0 || section.Link >= uint32(len(image.file.Sections)) {
			return errors.New("module ELF: malformed symbol table")
		}
		strings := image.file.Sections[section.Link]
		if strings.Type != elf.SHT_STRTAB {
			return errors.New("module ELF: invalid symbol string table")
		}
		if section.Type != elf.SHT_DYNSYM {
			continue
		}
		for _, field := range []struct {
			tag   elf.DynTag
			value uint64
		}{
			{elf.DT_SYMTAB, section.Addr}, {elf.DT_SYMENT, 24},
			{elf.DT_STRTAB, strings.Addr}, {elf.DT_STRSZ, strings.Size},
		} {
			value, present, err := image.singleTag(field.tag)
			if err != nil || !present || value != field.value {
				return errors.New("module ELF: dynamic and section symbol tables disagree")
			}
		}
		for _, data := range []*elf.Section{section, strings} {
			program, err := image.load(data.Addr, data.Size)
			if err != nil || program.Off+data.Addr-program.Vaddr != data.Offset {
				return errors.New("module ELF: symbol table is not mapped from declared bytes")
			}
		}
	}
	return nil
}
