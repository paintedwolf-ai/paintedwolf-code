package packagegeneration

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
)

const runtimeModuleELFLimit = 32 * 1024 * 1024

type runtimeModuleELF struct {
	raw          []byte
	file         *elf.File
	architecture string
	dynamic      map[elf.DynTag][]uint64
	symbols      []elf.Symbol
}

// Validate raw table extents before debug/elf follows its string/section tables.
func runtimeELF(raw []byte) (*runtimeModuleELF, error) {
	if len(raw) < 64 || len(raw) > runtimeModuleELFLimit ||
		!bytes.Equal(raw[:7], []byte{0x7f, 'E', 'L', 'F', 2, 1, 1}) ||
		binary.LittleEndian.Uint16(raw[16:]) != uint16(elf.ET_DYN) || binary.LittleEndian.Uint32(raw[20:]) != 1 ||
		binary.LittleEndian.Uint16(raw[52:]) != 64 {
		return nil, errors.New("module ELF: unsupported image")
	}
	architecture := ""
	switch elf.Machine(binary.LittleEndian.Uint16(raw[18:])) {
	case elf.EM_X86_64:
		architecture = "x86_64"
	case elf.EM_AARCH64:
		architecture = "aarch64"
	default:
		return nil, errors.New("module ELF: unsupported machine")
	}
	phoff, shoff := binary.LittleEndian.Uint64(raw[32:]), binary.LittleEndian.Uint64(raw[40:])
	phnum, shnum := binary.LittleEndian.Uint16(raw[56:]), binary.LittleEndian.Uint16(raw[60:])
	if binary.LittleEndian.Uint16(raw[54:]) != 56 || phnum == 0 || phnum > 64 ||
		binary.LittleEndian.Uint16(raw[58:]) != 64 || shnum == 0 || shnum > 4096 ||
		binary.LittleEndian.Uint16(raw[62:]) >= shnum ||
		!runtimeELFRange(raw, phoff, uint64(phnum)*56) || !runtimeELFRange(raw, shoff, uint64(shnum)*64) {
		return nil, errors.New("module ELF: invalid table bounds")
	}
	for index := range uint64(phnum) {
		row := raw[phoff+index*56:]
		kind, offset := elf.ProgType(binary.LittleEndian.Uint32(row)), binary.LittleEndian.Uint64(row[8:])
		address, fileSize, memorySize := binary.LittleEndian.Uint64(row[16:]), binary.LittleEndian.Uint64(row[32:]), binary.LittleEndian.Uint64(row[40:])
		if !runtimeELFRange(raw, offset, fileSize) || memorySize > ^uint64(0)-address ||
			kind == elf.PT_LOAD && fileSize > memorySize || kind == elf.PT_INTERP || kind == elf.PT_TLS {
			return nil, errors.New("module ELF: invalid or forbidden segment")
		}
	}
	for index := range uint64(shnum) {
		row := raw[shoff+index*64:]
		kind, offset, size := elf.SectionType(binary.LittleEndian.Uint32(row[4:])), binary.LittleEndian.Uint64(row[24:]), binary.LittleEndian.Uint64(row[32:])
		if kind != elf.SHT_NOBITS && !runtimeELFRange(raw, offset, size) ||
			binary.LittleEndian.Uint64(row[8:])&uint64(elf.SHF_COMPRESSED) != 0 {
			return nil, errors.New("module ELF: invalid or compressed section")
		}
	}
	file, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("module ELF: parse: %w", err)
	}
	image := &runtimeModuleELF{raw: raw, file: file, architecture: architecture, dynamic: make(map[elf.DynTag][]uint64)}
	if err := image.inspect(); err != nil {
		return nil, err
	}
	return image, nil
}

func runtimeELFRange(raw []byte, offset, size uint64) bool {
	return offset <= uint64(len(raw)) && size <= uint64(len(raw))-offset
}

func (image *runtimeModuleELF) inspect() error {
	dynamicCount, symbolTables := 0, 0
	for _, program := range image.file.Progs {
		if program.Type != elf.PT_DYNAMIC {
			continue
		}
		dynamicCount++
		if err := image.readDynamicTable(program); err != nil {
			return err
		}
	}
	if dynamicCount != 1 {
		return errors.New("module ELF: dynamic table must be unique")
	}
	for _, section := range image.file.Sections {
		if section.Type == elf.SHT_DYNSYM {
			symbolTables++
		}
		if section.Type == elf.SHT_REL || section.Type == elf.SHT_RELA {
			if err := image.checkRelocations(section); err != nil {
				return err
			}
		}
	}
	if symbolTables != 1 {
		return errors.New("module ELF: dynamic symbols must be unique")
	}
	if err := image.checkSymbolTables(); err != nil {
		return err
	}
	var err error
	image.symbols, err = image.file.DynamicSymbols()
	if err != nil {
		return err
	}
	for _, symbol := range image.symbols {
		if symbol.Name != "" && symbol.Section == elf.SHN_UNDEF {
			return errors.New("module ELF: undefined dynamic symbol")
		}
	}
	return image.checkDynamicRelocations()
}

func (image *runtimeModuleELF) checkRelocations(section *elf.Section) error {
	width := uint64(16)
	if section.Type == elf.SHT_RELA {
		width = 24
	}
	if section.Entsize != width || section.Size%width != 0 {
		return errors.New("module ELF: invalid relocation table")
	}
	for offset := section.Offset; offset < section.Offset+section.Size; offset += width {
		kind := uint32(binary.LittleEndian.Uint64(image.raw[offset+8:]))
		if image.runtimeIFUNCRelocation(kind) {
			return errors.New("module ELF: IFUNC relocation")
		}
	}
	return nil
}
