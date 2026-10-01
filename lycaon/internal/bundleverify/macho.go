// Package bundleverify audits native application bundles.
package bundleverify

import (
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// These load commands require direct decoding.
const (
	loadCmdVersionMinMacOSX = 0x24
	loadCmdBuildVersion     = 0x32
)

// SliceFacts is one architecture slice of a Mach-O file.
type SliceFacts struct {
	GoArch string   // "arm64", "amd64"; "" when the cputype is not one we ship
	MinOS  string   // "13.0"; "" when the file declares no minimum
	Dylibs []string // LC_LOAD_DYLIB names, verbatim
}

// MachOFacts is everything the checks need from one Mach-O file.
type MachOFacts struct {
	Path   string       // absolute
	Fat    bool         // true when the file is a universal binary
	Slices []SliceFacts // one per architecture
}

// isMachOMagic filters non-native payloads before parsing.
func isMachOMagic(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	switch binary.BigEndian.Uint32(b) {
	case 0xfeedface, 0xfeedfacf, // thin, big-endian magic
		0xcefaedfe, 0xcffaedfe, // thin, little-endian magic
		0xcafebabe, 0xbebafeca: // fat, both byte orders
		return true
	}
	return false
}

// InspectMachO returns nil facts for non-native files.
func InspectMachO(path string) (*MachOFacts, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	n, err := io.ReadFull(f, header)
	closeErr := f.Close()
	if err != nil && n < 4 {
		return nil, nil
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !isMachOMagic(header) {
		return nil, nil
	}

	if facts, err := inspectFat(path); err == nil {
		return facts, nil
	}
	return inspectThin(path)
}

func inspectFat(path string) (*MachOFacts, error) {
	fat, err := macho.OpenFat(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fat.Close() }()

	facts := &MachOFacts{Path: path, Fat: true}
	for i := range fat.Arches {
		facts.Slices = append(facts.Slices, sliceFacts(fat.Arches[i].File))
	}
	return facts, nil
}

func inspectThin(path string) (*MachOFacts, error) {
	f, err := macho.Open(path)
	if err != nil {
		return nil, fmt.Errorf("parse mach-o %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	return &MachOFacts{Path: path, Slices: []SliceFacts{sliceFacts(f)}}, nil
}

func sliceFacts(f *macho.File) SliceFacts {
	dylibs, err := f.ImportedLibraries()
	if err != nil {
		dylibs = nil
	}
	return SliceFacts{
		GoArch: goArchForCPU(f.Cpu),
		MinOS:  minOSVersion(f),
		Dylibs: dylibs,
	}
}

func goArchForCPU(cpu macho.Cpu) string {
	switch cpu {
	case macho.CpuArm64:
		return "arm64"
	case macho.CpuAmd64:
		return "amd64"
	default:
		return ""
	}
}

// minOSVersion prefers LC_BUILD_VERSION over LC_VERSION_MIN_MACOSX.
func minOSVersion(f *macho.File) string {
	var fallback string
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) < 8 {
			continue
		}
		switch f.ByteOrder.Uint32(raw[0:4]) {
		case loadCmdBuildVersion:
			// cmd, cmdsize, platform, minos, sdk, ntools
			if len(raw) >= 16 {
				return decodeVersion(f.ByteOrder.Uint32(raw[12:16]))
			}
		case loadCmdVersionMinMacOSX:
			// cmd, cmdsize, version, sdk
			if len(raw) >= 12 && fallback == "" {
				fallback = decodeVersion(f.ByteOrder.Uint32(raw[8:12]))
			}
		}
	}
	return fallback
}

// decodeVersion renders the packed release floor at major.minor precision.
func decodeVersion(v uint32) string {
	return fmt.Sprintf("%d.%d", v>>16, (v>>8)&0xff)
}
