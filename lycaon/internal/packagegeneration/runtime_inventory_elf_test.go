package packagegeneration

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRuntimeInventoryELFRejectsUnboundedAndTruncatedInput(t *testing.T) {
	for name, raw := range map[string][]byte{"empty": nil, "truncated": []byte("\x7fELF"),
		"oversized": make([]byte, runtimeModuleELFLimit+1)} {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectRuntimeModuleABI(raw, raw); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_DYN))
	binary.LittleEndian.PutUint16(header[18:], uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	binary.LittleEndian.PutUint16(header[54:], 56)
	binary.LittleEndian.PutUint16(header[56:], 1)
	binary.LittleEndian.PutUint16(header[58:], 64)
	binary.LittleEndian.PutUint16(header[60:], 1)
	binary.LittleEndian.PutUint64(header[32:], ^uint64(0)-8)
	if _, err := runtimeELF(header); err == nil {
		t.Fatal("overflowing ELF table pointer accepted")
	}
}

// These unit fixtures test slot metadata relationships, not native ELF linking.
func slotMetadataFixture() *runtimeModuleELF {
	raw := make([]byte, 56)
	copy(raw[8:], runtimeDigestMarker)
	return &runtimeModuleELF{raw: raw,
		file: &elf.File{
			Sections: []*elf.Section{{}, {SectionHeader: elf.SectionHeader{Type: elf.SHT_PROGBITS,
				Flags: elf.SHF_ALLOC, Addr: 0x1000, Offset: 8, Size: 48}}},
			Progs: []*elf.Prog{{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Flags: elf.PF_R,
				Off: 8, Vaddr: 0x1000, Filesz: 48, Memsz: 48}}},
		},
		symbols: []elf.Symbol{{Name: "slot", Info: 0x11, Section: 1, Value: 0x1000, Size: 48}},
	}
}

func TestRuntimeInventorySlotRequiresUniqueReadOnlyPhysicalBytes(t *testing.T) {
	image := slotMetadataFixture()
	selected, err := image.slot("slot", 48, runtimeDigestMarker, true)
	testutil.FailErr(t, "select exact slot metadata", err)
	if selected.offset != 8 || selected.address != 0x1000 {
		t.Fatal("slot did not map virtual address to retained file bytes")
	}
	for name, mutate := range map[string]func(*runtimeModuleELF){
		"duplicate symbol":      func(v *runtimeModuleELF) { v.symbols = append(v.symbols, v.symbols[0]) },
		"undefined symbol":      func(v *runtimeModuleELF) { v.symbols[0].Section = elf.SHN_UNDEF },
		"absolute symbol":       func(v *runtimeModuleELF) { v.symbols[0].Section = elf.SHN_ABS },
		"wrong symbol type":     func(v *runtimeModuleELF) { v.symbols[0].Info = 0x12 },
		"hidden symbol":         func(v *runtimeModuleELF) { v.symbols[0].Other = 2 },
		"wrong size":            func(v *runtimeModuleELF) { v.symbols[0].Size-- },
		"writable load":         func(v *runtimeModuleELF) { v.file.Progs[0].Flags |= elf.PF_W },
		"writable section":      func(v *runtimeModuleELF) { v.file.Sections[1].Flags |= elf.SHF_WRITE },
		"nobits section":        func(v *runtimeModuleELF) { v.file.Sections[1].Type = elf.SHT_NOBITS },
		"short file backing":    func(v *runtimeModuleELF) { v.file.Progs[0].Filesz-- },
		"section load mismatch": func(v *runtimeModuleELF) { v.file.Sections[1].Offset-- },
		"overlapping loads":     func(v *runtimeModuleELF) { v.file.Progs = append(v.file.Progs, v.file.Progs[0]) },
		"bad marker":            func(v *runtimeModuleELF) { v.raw[8] ^= 1 },
		"already bound":         func(v *runtimeModuleELF) { v.raw[55] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := slotMetadataFixture()
			mutate(candidate)
			if _, err := candidate.slot("slot", 48, runtimeDigestMarker, true); err == nil {
				t.Fatal("unsafe source slot accepted")
			}
		})
	}
	image.raw[55] = 1
	_, err = image.slot("slot", 48, runtimeDigestMarker, false)
	testutil.FailErr(t, "inspect final bound slot without changing it", err)
	if !bytes.Equal(image.raw[8:24], runtimeDigestMarker) {
		t.Fatal("inspection changed anchor bytes")
	}
}

func TestRuntimeInventoryChecksLoaderSelectedIFUNCRelocations(t *testing.T) {
	for architecture, ifunc := range map[string]uint32{"x86_64": uint32(elf.R_X86_64_IRELATIVE), "aarch64": uint32(elf.R_AARCH64_IRELATIVE)} {
		t.Run(architecture, func(t *testing.T) {
			image := &runtimeModuleELF{raw: make([]byte, 24), architecture: architecture,
				file: &elf.File{Progs: []*elf.Prog{{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD,
					Vaddr: 0x1000, Filesz: 24, Memsz: 24}}}},
				dynamic: map[elf.DynTag][]uint64{elf.DT_RELA: {0x1000}, elf.DT_RELASZ: {24}, elf.DT_RELAENT: {24}}}
			testutil.FailErr(t, "inspect non-resolver relocations", image.checkDynamicRelocations())
			binary.LittleEndian.PutUint64(image.raw[8:], uint64(ifunc))
			if err := image.checkDynamicRelocations(); err == nil {
				t.Fatal("loader-selected IFUNC hidden from sections accepted")
			}
			image.dynamic[elf.DT_RELA] = append(image.dynamic[elf.DT_RELA], 0x1000)
			if err := image.checkDynamicRelocations(); err == nil {
				t.Fatal("ambiguous dynamic relocation pointer accepted")
			}
		})
	}
}
