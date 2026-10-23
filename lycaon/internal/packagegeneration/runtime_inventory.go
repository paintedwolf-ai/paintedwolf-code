package packagegeneration

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
)

// These are the existing embedded C/Python format ceilings, not capture policy.
const RuntimeInventoryCapacity = 4 * 1024 * 1024
const RuntimeInventoryMaxEntries = 32768
const runtimeInventoryHeader = 40
const runtimeInventoryEntry = 42
const runtimeGuardOffsetLimit = 64 * 1024 * 1024

var runtimeInventoryMagic = []byte("PWMIV01\x00")

type RuntimeModule struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// RuntimeInventory authenticates no roots. Its caller selects immutable bytes
// and authenticates the final auditor and guard through the generation seal.
type RuntimeInventory struct {
	Architecture         string          `json:"architecture"`
	Guard                string          `json:"guard"`
	GuardWitnessOffset   uint64          `json:"guard_witness_offset"`
	GuardInventoryOffset uint64          `json:"guard_inventory_offset"`
	Entries              []RuntimeModule `json:"entries"`
}

func EncodeRuntimeInventory(inventory *RuntimeInventory) ([]byte, error) {
	if inventory == nil || !runtimeAbsolute(inventory.Guard) ||
		!runtimeGuardOffset(inventory.GuardWitnessOffset) || !runtimeGuardOffset(inventory.GuardInventoryOffset) ||
		len(inventory.Entries) == 0 || len(inventory.Entries) > RuntimeInventoryMaxEntries {
		return nil, errors.New("module inventory: invalid shape")
	}
	machine, err := runtimeInventoryMachine(inventory.Architecture)
	if err != nil {
		return nil, err
	}
	entries := slices.Clone(inventory.Entries)
	slices.SortFunc(entries, func(a, b RuntimeModule) int { return strings.Compare(a.Path, b.Path) })
	size, previous := runtimeInventoryHeader+len(inventory.Guard), ""
	for _, entry := range entries {
		if !runtimeAbsolute(entry.Path) || entry.Path == inventory.Guard || entry.Path == previous ||
			entry.Size <= 0 || !runtimeHash(entry.SHA256) {
			return nil, errors.New("module inventory: invalid or duplicate module")
		}
		size += runtimeInventoryEntry + len(entry.Path)
		previous = entry.Path
	}
	if size > RuntimeInventoryCapacity-16 {
		return nil, errors.New("module inventory: embedded capacity exceeded")
	}
	raw := make([]byte, size)
	copy(raw, runtimeInventoryMagic)
	binary.LittleEndian.PutUint32(raw[8:], uint32(size))
	binary.LittleEndian.PutUint32(raw[12:], uint32(len(entries)))
	binary.LittleEndian.PutUint64(raw[16:], inventory.GuardWitnessOffset)
	binary.LittleEndian.PutUint64(raw[24:], inventory.GuardInventoryOffset)
	binary.LittleEndian.PutUint16(raw[32:], uint16(len(inventory.Guard)))
	binary.LittleEndian.PutUint16(raw[34:], machine)
	cursor := runtimeInventoryHeader + copy(raw[runtimeInventoryHeader:], inventory.Guard)
	for _, entry := range entries {
		binary.LittleEndian.PutUint64(raw[cursor:], uint64(entry.Size))
		digest, _ := hex.DecodeString(entry.SHA256) // validated lowercase digest above
		copy(raw[cursor+8:], digest)
		binary.LittleEndian.PutUint16(raw[cursor+40:], uint16(len(entry.Path)))
		cursor += runtimeInventoryEntry + copy(raw[cursor+runtimeInventoryEntry:], entry.Path)
	}
	return raw, nil
}

func DecodeRuntimeInventory(raw []byte) (*RuntimeInventory, error) {
	if len(raw) < runtimeInventoryHeader || len(raw) > RuntimeInventoryCapacity-16 ||
		!bytes.Equal(raw[:8], runtimeInventoryMagic) || int(binary.LittleEndian.Uint32(raw[8:])) != len(raw) ||
		!bytes.Equal(raw[36:40], []byte{0, 0, 0, 0}) {
		return nil, errors.New("module inventory: invalid header")
	}
	count := binary.LittleEndian.Uint32(raw[12:])
	if count == 0 || count > RuntimeInventoryMaxEntries {
		return nil, errors.New("module inventory: invalid count")
	}
	architecture := ""
	switch binary.LittleEndian.Uint16(raw[34:]) {
	case 62:
		architecture = "x86_64"
	case 183:
		architecture = "aarch64"
	default:
		return nil, errors.New("module inventory: unsupported architecture")
	}
	guardSize := int(binary.LittleEndian.Uint16(raw[32:]))
	if guardSize > len(raw)-runtimeInventoryHeader {
		return nil, errors.New("module inventory: truncated guard")
	}
	inventory := &RuntimeInventory{Architecture: architecture,
		Guard: string(raw[40 : 40+guardSize]), GuardWitnessOffset: binary.LittleEndian.Uint64(raw[16:]),
		GuardInventoryOffset: binary.LittleEndian.Uint64(raw[24:])}
	cursor, previous := runtimeInventoryHeader+guardSize, ""
	for range count {
		if len(raw)-cursor < runtimeInventoryEntry {
			return nil, errors.New("module inventory: truncated entry")
		}
		size := binary.LittleEndian.Uint64(raw[cursor:])
		nameSize := int(binary.LittleEndian.Uint16(raw[cursor+40:]))
		if nameSize > len(raw)-cursor-runtimeInventoryEntry || size == 0 || size >= 1<<63 {
			return nil, errors.New("module inventory: invalid entry size")
		}
		name := string(raw[cursor+runtimeInventoryEntry : cursor+runtimeInventoryEntry+nameSize])
		if name <= previous {
			return nil, errors.New("module inventory: noncanonical entry order")
		}
		inventory.Entries = append(inventory.Entries, RuntimeModule{Path: name, Size: int64(size),
			SHA256: hex.EncodeToString(raw[cursor+8 : cursor+40])})
		cursor += runtimeInventoryEntry + nameSize
		previous = name
	}
	canonical, err := EncodeRuntimeInventory(inventory)
	if err != nil {
		return nil, err
	}
	if cursor != len(raw) || !bytes.Equal(canonical, raw) {
		return nil, errors.New("module inventory: trailing or noncanonical bytes")
	}
	return inventory, nil
}

func runtimeGuardOffset(value uint64) bool {
	return value > 0 && value <= runtimeGuardOffsetLimit && value%8 == 0
}

func runtimeInventoryMachine(architecture string) (uint16, error) {
	switch architecture {
	case "x86_64":
		return 62, nil
	case "aarch64":
		return 183, nil
	default:
		return 0, errors.New("module inventory: unsupported architecture")
	}
}
