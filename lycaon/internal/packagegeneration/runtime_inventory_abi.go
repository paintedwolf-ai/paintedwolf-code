package packagegeneration

import (
	"debug/elf"
	"errors"
)

type RuntimeModuleABI struct {
	SchemaVersion        int    `json:"schema_version"`
	Architecture         string `json:"architecture"`
	GuardWitnessOffset   uint64 `json:"guard_witness_offset"`
	GuardInventoryOffset uint64 `json:"guard_inventory_offset"`
	AuditorSHA256        string `json:"auditor_sha256"`
	GuardSHA256          string `json:"guard_sha256"`
}

// InspectRuntimeModuleABI checks final byte structure, not source custody.
func InspectRuntimeModuleABI(auditor, guard []byte) (RuntimeModuleABI, error) {
	audit, err := runtimeELF(auditor)
	if err != nil {
		return RuntimeModuleABI{}, err
	}
	gate, err := runtimeELF(guard)
	if err != nil {
		return RuntimeModuleABI{}, err
	}
	if audit.architecture != gate.architecture {
		return RuntimeModuleABI{}, errors.New("module ABI: architectures differ")
	}
	for _, name := range []string{"la_version", "la_objsearch", "la_objopen"} {
		symbol, err := audit.symbol(name)
		if err != nil || symbol.Info != 0x12 || symbol.Other != 0 || symbol.Section == elf.SHN_UNDEF {
			return RuntimeModuleABI{}, errors.New("module ABI: auditor callback missing or ambiguous")
		}
	}
	for _, tag := range []elf.DynTag{elf.DT_INIT, elf.DT_INIT_ARRAY, elf.DT_INIT_ARRAYSZ, elf.DT_PREINIT_ARRAY, elf.DT_PREINIT_ARRAYSZ} {
		for _, value := range audit.dynamic[tag] {
			if value != 0 {
				return RuntimeModuleABI{}, errors.New("module ABI: auditor initialization callback")
			}
		}
	}
	if err := audit.checkFunctions(); err != nil {
		return RuntimeModuleABI{}, err
	}
	if err := gate.checkFunctions(); err != nil {
		return RuntimeModuleABI{}, err
	}
	flags := gate.dynamic[elf.DT_FLAGS_1]
	if len(flags) != 1 || flags[0]&uint64(elf.DF_1_INITFIRST) == 0 {
		return RuntimeModuleABI{}, errors.New("module ABI: guard requires INITFIRST")
	}
	witness, err := gate.symbol("pw_module_guard_witness_v2")
	if err != nil || witness.Info != 0x11 || witness.Other != 0 || witness.Section == elf.SHN_UNDEF ||
		witness.Size != 8 || !runtimeGuardOffset(witness.Value) {
		return RuntimeModuleABI{}, errors.New("module ABI: invalid guard witness")
	}
	load, err := gate.load(witness.Value, 8)
	if err != nil || load.Flags&elf.PF_W == 0 || !gate.relro(witness.Value, 8) {
		return RuntimeModuleABI{}, errors.New("module ABI: witness must be writable RELRO")
	}
	offset := load.Off + witness.Value - load.Vaddr
	for _, value := range guard[offset : offset+8] {
		if value != 0 {
			return RuntimeModuleABI{}, errors.New("module ABI: guard witness is not initially zero")
		}
	}
	digest, err := gate.slot("pw_module_inventory_digest_v1", 48, runtimeDigestMarker, false)
	if err != nil {
		return RuntimeModuleABI{}, err
	}
	if !runtimeGuardOffset(digest.address) {
		return RuntimeModuleABI{}, errors.New("module ABI: invalid guard inventory offset")
	}
	if _, err := audit.slot("pw_module_inventory_v1", RuntimeInventoryCapacity, runtimeInventoryMarker, false); err != nil {
		return RuntimeModuleABI{}, err
	}
	if _, err := audit.slot("pw_module_guard_digest_v1", 48, runtimeGuardMarker, false); err != nil {
		return RuntimeModuleABI{}, err
	}
	return RuntimeModuleABI{SchemaVersion: 2, Architecture: audit.architecture,
		GuardWitnessOffset: witness.Value, GuardInventoryOffset: digest.address,
		AuditorSHA256: runtimeDigest(auditor), GuardSHA256: runtimeDigest(guard)}, nil
}

func (image *runtimeModuleELF) symbol(name string) (elf.Symbol, error) {
	var selected elf.Symbol
	count := 0
	for _, symbol := range image.symbols {
		if symbol.Name == name {
			selected, count = symbol, count+1
		}
	}
	if count != 1 {
		return elf.Symbol{}, errors.New("module ABI: symbol must be unique")
	}
	return selected, nil
}

func (image *runtimeModuleELF) checkFunctions() error {
	static, err := image.file.Symbols()
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return err
	}
	for _, symbols := range [][]elf.Symbol{image.symbols, static} {
		for _, symbol := range symbols {
			if elf.ST_TYPE(symbol.Info) == elf.STT_GNU_IFUNC {
				return errors.New("module ABI: IFUNC symbol")
			}
			switch symbol.Name {
			case "malloc", "calloc", "realloc", "free", "__rust_alloc", "__rust_realloc", "__rust_alloc_zeroed", "getauxval", "__getauxval":
				return errors.New("module ABI: allocator or feature probe")
			}
		}
	}
	return nil
}

func (image *runtimeModuleELF) load(address, size uint64) (*elf.Prog, error) {
	var selected *elf.Prog
	for _, program := range image.file.Progs {
		if program.Type != elf.PT_LOAD || address < program.Vaddr ||
			address-program.Vaddr > program.Filesz || size > program.Filesz-(address-program.Vaddr) {
			continue
		}
		if selected != nil {
			return nil, errors.New("module ABI: ambiguous load segment")
		}
		selected = program
	}
	if selected == nil {
		return nil, errors.New("module ABI: object is not file-backed")
	}
	return selected, nil
}

func (image *runtimeModuleELF) relro(address, size uint64) bool {
	for _, program := range image.file.Progs {
		if program.Type == elf.PT_GNU_RELRO && address >= program.Vaddr &&
			address-program.Vaddr <= program.Memsz && size <= program.Memsz-(address-program.Vaddr) {
			return true
		}
	}
	return false
}
