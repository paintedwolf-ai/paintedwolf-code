package packagegeneration

import (
	"debug/elf"
	"encoding/binary"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func dynamicMetadataFixture() (*runtimeModuleELF, *elf.Prog) {
	image := &runtimeModuleELF{raw: make([]byte, 64), dynamic: make(map[elf.DynTag][]uint64),
		file: &elf.File{Progs: []*elf.Prog{
			{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Flags: elf.PF_R | elf.PF_W,
				Vaddr: 0x1000, Off: 0, Filesz: 32, Memsz: 32}},
			{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Flags: elf.PF_R | elf.PF_W,
				Vaddr: 0x2000, Off: 32, Filesz: 32, Memsz: 32}},
		}}}
	program := &elf.Prog{ProgHeader: elf.ProgHeader{Type: elf.PT_DYNAMIC, Flags: elf.PF_R | elf.PF_W,
		Vaddr: 0x1000, Off: 0, Filesz: 32, Memsz: 32}}
	return image, program
}

func TestRuntimeInventoryDynamicTableUsesLoaderSelectedBytes(t *testing.T) {
	image, program := dynamicMetadataFixture()
	testutil.FailErr(t, "accept mapped writable dynamic table", image.readDynamicTable(program))
	// Offset zero is benign, but the forged virtual address selects DT_NEEDED.
	binary.LittleEndian.PutUint64(image.raw[32:], uint64(elf.DT_NEEDED))
	program.Vaddr = 0x2000
	if err := image.readDynamicTable(program); err == nil {
		t.Fatal("benign p_offset concealed loader-selected dependency")
	}
	program.Off = 32
	if err := image.readDynamicTable(program); err == nil {
		t.Fatal("dependency accepted after binding the correct mapped offset")
	}
}

func TestRuntimeInventoryDynamicTableRejectsExternalCodeTags(t *testing.T) {
	for _, tag := range []elf.DynTag{elf.DT_NEEDED, elf.DT_FILTER, elf.DT_AUXILIARY, elf.DT_AUDIT, elf.DT_DEPAUDIT} {
		t.Run(tag.String(), func(t *testing.T) {
			image, program := dynamicMetadataFixture()
			binary.LittleEndian.PutUint64(image.raw, uint64(tag))
			if err := image.readDynamicTable(program); err == nil {
				t.Fatal("external code selection without ordinary dependency accepted")
			}
		})
	}
}

func TestRuntimeInventoryDynamicTableRequiresConsistentHeaderAndMapping(t *testing.T) {
	for name, mutate := range map[string]func(*runtimeModuleELF, *elf.Prog){
		"memory extent":      func(_ *runtimeModuleELF, p *elf.Prog) { p.Memsz += 16 },
		"zero bytes":         func(_ *runtimeModuleELF, p *elf.Prog) { p.Filesz = 0 },
		"partial entry":      func(_ *runtimeModuleELF, p *elf.Prog) { p.Filesz--; p.Memsz-- },
		"outside image":      func(_ *runtimeModuleELF, p *elf.Prog) { p.Off = 64 },
		"unmapped address":   func(_ *runtimeModuleELF, p *elf.Prog) { p.Vaddr = 0x3000 },
		"ambiguous load":     func(v *runtimeModuleELF, _ *elf.Prog) { v.file.Progs = append(v.file.Progs, v.file.Progs[0]) },
		"unreadable table":   func(_ *runtimeModuleELF, p *elf.Prog) { p.Flags = elf.PF_W },
		"inconsistent flags": func(v *runtimeModuleELF, _ *elf.Prog) { v.file.Progs[0].Flags = elf.PF_R },
		"no terminator": func(v *runtimeModuleELF, _ *elf.Prog) {
			binary.LittleEndian.PutUint64(v.raw, uint64(elf.DT_DEBUG))
			binary.LittleEndian.PutUint64(v.raw[16:], uint64(elf.DT_DEBUG))
		},
	} {
		t.Run(name, func(t *testing.T) {
			image, program := dynamicMetadataFixture()
			mutate(image, program)
			if err := image.readDynamicTable(program); err == nil {
				t.Fatal("inconsistent dynamic table accepted")
			}
		})
	}
}
