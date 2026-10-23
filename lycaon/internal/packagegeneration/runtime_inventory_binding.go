package packagegeneration

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"errors"
)

var runtimeInventoryMarker = []byte("PW-INV-SLOT-V1\x00\x00")
var runtimeDigestMarker = []byte("PW-HASH-SLOT-V1\x00")
var runtimeGuardMarker = []byte("PW-GUARD-HASH-V1")

type runtimeModuleSlot struct {
	offset  uint64
	size    uint64
	address uint64
}

func (image *runtimeModuleELF) slot(name string, size uint64, marker []byte, unbound bool) (runtimeModuleSlot, error) {
	symbol, err := image.symbol(name)
	if err != nil || symbol.Info != 0x11 || symbol.Other != 0 || symbol.Size != size ||
		symbol.Section == elf.SHN_UNDEF || int(symbol.Section) >= len(image.file.Sections) {
		return runtimeModuleSlot{}, errors.New("module binding: invalid slot symbol")
	}
	section := image.file.Sections[symbol.Section]
	if section.Type == elf.SHT_NOBITS || section.Flags&elf.SHF_ALLOC == 0 || section.Flags&elf.SHF_WRITE != 0 ||
		symbol.Value < section.Addr || symbol.Value-section.Addr > section.Size || size > section.Size-(symbol.Value-section.Addr) {
		return runtimeModuleSlot{}, errors.New("module binding: slot section is not read-only file-backed")
	}
	load, err := image.load(symbol.Value, size)
	if err != nil || load.Flags&elf.PF_W != 0 || load.Flags&elf.PF_R == 0 {
		return runtimeModuleSlot{}, errors.New("module binding: slot load is not read-only")
	}
	offset := load.Off + symbol.Value - load.Vaddr
	if section.Offset+symbol.Value-section.Addr != offset || !runtimeELFRange(image.raw, offset, size) {
		return runtimeModuleSlot{}, errors.New("module binding: slot section/load disagree")
	}
	data := image.raw[offset : offset+size]
	if len(marker) > len(data) || !bytes.Equal(data[:len(marker)], marker) {
		return runtimeModuleSlot{}, errors.New("module binding: invalid slot marker")
	}
	if unbound {
		for _, value := range data[len(marker):] {
			if value != 0 {
				return runtimeModuleSlot{}, errors.New("module binding: source slot is already bound")
			}
		}
	}
	return runtimeModuleSlot{offset: offset, size: size, address: symbol.Value}, nil
}

type BoundRuntimeModules struct {
	Auditor            []byte
	Guard              []byte
	Inventory          []byte
	InputAuditorSHA256 string
	InputGuardSHA256   string
	InventorySHA256    string
	FinalABI           RuntimeModuleABI
}

// BindRuntimeModules returns candidate copies, never changing source slices or
// publishing bytes. The root transaction independently authenticates its inputs.
func BindRuntimeModules(auditor, guard, inventory []byte) (*BoundRuntimeModules, error) {
	selected, err := DecodeRuntimeInventory(inventory)
	if err != nil {
		return nil, err
	}
	original, err := InspectRuntimeModuleABI(auditor, guard)
	if err != nil {
		return nil, err
	}
	if original.Architecture != selected.Architecture || original.GuardWitnessOffset != selected.GuardWitnessOffset ||
		original.GuardInventoryOffset != selected.GuardInventoryOffset {
		return nil, errors.New("module binding: inventory differs from guard ABI")
	}
	audit, err := runtimeELF(auditor)
	if err != nil {
		return nil, err
	}
	gate, err := runtimeELF(guard)
	if err != nil {
		return nil, err
	}
	auditSlot, err := audit.slot("pw_module_inventory_v1", RuntimeInventoryCapacity, runtimeInventoryMarker, true)
	if err != nil {
		return nil, err
	}
	guardHash, err := audit.slot("pw_module_guard_digest_v1", 48, runtimeGuardMarker, true)
	if err != nil {
		return nil, err
	}
	gateSlot, err := gate.slot("pw_module_inventory_digest_v1", 48, runtimeDigestMarker, true)
	if err != nil {
		return nil, err
	}
	result := &BoundRuntimeModules{Auditor: bytes.Clone(auditor), Guard: bytes.Clone(guard), Inventory: bytes.Clone(inventory),
		InputAuditorSHA256: original.AuditorSHA256, InputGuardSHA256: original.GuardSHA256, InventorySHA256: runtimeDigest(inventory)}
	copy(result.Auditor[auditSlot.offset+16:auditSlot.offset+auditSlot.size], inventory)
	inventoryHash := sha256.Sum256(inventory)
	copy(result.Guard[gateSlot.offset+16:gateSlot.offset+gateSlot.size], inventoryHash[:])
	// Acyclic: guard contains ordinary-inventory hash; auditor then binds the
	// complete final guard. The final generation seal authenticates the auditor.
	guardDigest := sha256.Sum256(result.Guard)
	copy(result.Auditor[guardHash.offset+16:guardHash.offset+guardHash.size], guardDigest[:])
	result.FinalABI, err = InspectRuntimeModuleABI(result.Auditor, result.Guard)
	if err != nil {
		return nil, err
	}
	return result, nil
}
