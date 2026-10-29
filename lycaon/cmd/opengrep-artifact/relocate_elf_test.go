package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/onefile"
	"github.com/lycaon/lycaon/internal/testutil"
)

type dynamicELFFixture struct {
	machine     elf.Machine
	interpreter bool
	dynamic     bool
	strings     []string
	entries     [][2]uint64
	truncate    bool
}

func nativeELFMachine() elf.Machine {
	if runtime.GOARCH == "arm64" {
		return elf.EM_AARCH64
	}
	return elf.EM_X86_64
}

// build lays out an ELF64 image whose dynamic section references a string table,
// matching what debug/elf reads for DT_NEEDED, DT_RPATH and DT_RUNPATH.
func (fixture dynamicELFFixture) build() []byte {
	table := []byte{0}
	offsets := make(map[string]uint64)
	for _, value := range fixture.strings {
		offsets[value] = uint64(len(table))
		table = append(append(table, value...), 0)
	}
	var dynamic []byte
	for _, entry := range fixture.entries {
		dynamic = binary.LittleEndian.AppendUint64(dynamic, entry[0])
		dynamic = binary.LittleEndian.AppendUint64(dynamic, entry[1])
	}
	dynamic = binary.LittleEndian.AppendUint64(dynamic, uint64(elf.DT_NULL))
	dynamic = binary.LittleEndian.AppendUint64(dynamic, 0)
	if fixture.truncate {
		dynamic = dynamic[:len(dynamic)-8]
	}
	var programs []uint32
	if fixture.interpreter {
		programs = append(programs, uint32(elf.PT_INTERP))
	}
	if fixture.dynamic {
		programs = append(programs, uint32(elf.PT_DYNAMIC))
	} else {
		programs = append(programs, uint32(elf.PT_LOAD))
	}
	programOffset := uint64(64)
	tableOffset := programOffset + uint64(56*len(programs))
	dynamicOffset := tableOffset + uint64(len(table))
	sectionOffset := dynamicOffset + uint64(len(dynamic))
	raw := make([]byte, sectionOffset+3*64)
	copy(raw, []byte{'\x7f', 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1})
	binary.LittleEndian.PutUint16(raw[16:], uint16(elf.ET_DYN))
	binary.LittleEndian.PutUint16(raw[18:], uint16(fixture.machine))
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[32:], programOffset)
	binary.LittleEndian.PutUint64(raw[40:], sectionOffset)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], uint16(len(programs)))
	binary.LittleEndian.PutUint16(raw[58:], 64)
	binary.LittleEndian.PutUint16(raw[60:], 3)
	binary.LittleEndian.PutUint16(raw[62:], 1)
	for index, kind := range programs {
		binary.LittleEndian.PutUint32(raw[programOffset+uint64(56*index):], kind)
	}
	copy(raw[tableOffset:], table)
	copy(raw[dynamicOffset:], dynamic)
	section := func(index int, kind elf.SectionType, offset, size uint64, link uint32) {
		header := raw[sectionOffset+uint64(64*index):]
		binary.LittleEndian.PutUint32(header[4:], uint32(kind))
		binary.LittleEndian.PutUint64(header[24:], offset)
		binary.LittleEndian.PutUint64(header[32:], size)
		binary.LittleEndian.PutUint32(header[40:], link)
	}
	section(1, elf.SHT_STRTAB, tableOffset, uint64(len(table)), 0)
	section(2, elf.SHT_DYNAMIC, dynamicOffset, uint64(len(dynamic)), 1)
	return raw
}

func relocationToolFixture(t *testing.T, script string) string {
	t.Helper()
	tool := filepath.Join(t.TempDir(), "patch-fixture")
	testutil.FailErr(t, "write relocation tool fixture", os.WriteFile(tool, []byte(script), 0o700))
	return tool
}

func TestConsumerELFRelocationRewritesOnlyDeclaredDynamicImages(t *testing.T) {
	tool := relocationToolFixture(t, "#!/bin/sh\nfor last do :; done\nprintf '\\n%s' \"$@\" >> \"$last\"\n")
	library := dynamicELFFixture{machine: nativeELFMachine(), dynamic: true}.build()
	relocator := consumerELFRelocator{ctx: t.Context(), loader: "/nix/store/fixture-glibc/lib/ld.so", roots: []string{"/nix/store/fixture-glibc/lib"},
		scratch: t.TempDir(), members: map[string]onefile.Member{"bin/lib/libpayload.so": {Data: library}}}
	relocator.recipe.ToolPath = tool
	program := dynamicELFFixture{machine: nativeELFMachine(), interpreter: true, dynamic: true, strings: []string{"$ORIGIN/lib", "libpayload.so"},
		entries: [][2]uint64{{uint64(elf.DT_RUNPATH), 1}, {uint64(elf.DT_NEEDED), uint64(len("$ORIGIN/lib") + 2)}}}.build()
	relocated, err := relocator.relocate("bin/program", program)
	testutil.FailErr(t, "relocate declared dynamic image", err)
	if !bytes.HasPrefix(relocated, program) {
		t.Fatal("relocation did not return the patched image bytes")
	}
	arguments := strings.Split(strings.TrimPrefix(string(relocated[len(program):]), "\n"), "\n")
	if len(arguments) != 5 || arguments[0] != "--set-rpath" || arguments[1] != "$ORIGIN/lib:/nix/store/fixture-glibc/lib" ||
		arguments[2] != "--set-interpreter" || arguments[3] != relocator.loader {
		t.Fatalf("relocation tool received undeclared arguments: %q", arguments)
	}
	static := dynamicELFFixture{machine: nativeELFMachine()}.build()
	unchanged, err := relocator.relocate("bin/static", static)
	testutil.FailErr(t, "retain static image", err)
	if !bytes.Equal(unchanged, static) {
		t.Fatal("static image was rewritten")
	}
	data := []byte("not an executable image")
	unchanged, err = relocator.relocate("data/file", data)
	testutil.FailErr(t, "retain data member", err)
	if !bytes.Equal(unchanged, data) {
		t.Fatal("data member was rewritten")
	}
}

func TestConsumerELFRelocationRefusesUndeclaredImages(t *testing.T) {
	foreign := elf.EM_X86_64
	if nativeELFMachine() == elf.EM_X86_64 {
		foreign = elf.EM_AARCH64
	}
	for name, fixture := range map[string][]byte{
		"malformed header":    []byte("\x7fELF malformed"),
		"foreign machine":     dynamicELFFixture{machine: foreign, dynamic: true}.build(),
		"malformed dynamic":   dynamicELFFixture{machine: nativeELFMachine(), dynamic: true, truncate: true}.build(),
		"escaping RPATH":      dynamicELFFixture{machine: nativeELFMachine(), dynamic: true, strings: []string{"$ORIGIN/../.."}, entries: [][2]uint64{{uint64(elf.DT_RPATH), 1}}}.build(),
		"ambient RUNPATH":     dynamicELFFixture{machine: nativeELFMachine(), dynamic: true, strings: []string{"/usr/lib"}, entries: [][2]uint64{{uint64(elf.DT_RUNPATH), 1}}}.build(),
		"undeclared provider": dynamicELFFixture{machine: nativeELFMachine(), dynamic: true, strings: []string{"libc.so.6"}, entries: [][2]uint64{{uint64(elf.DT_NEEDED), 1}}}.build(),
	} {
		t.Run(name, func(t *testing.T) {
			tool := relocationToolFixture(t, "#!/bin/sh\nexit 0\n")
			relocator := consumerELFRelocator{ctx: t.Context(), loader: "/nix/store/fixture-glibc/lib/ld.so", roots: []string{t.TempDir()}, scratch: t.TempDir()}
			relocator.recipe.ToolPath = tool
			if _, err := relocator.relocate("bin/program", fixture); err == nil {
				t.Fatal("undeclared embedded image admitted")
			}
		})
	}
}

func TestConsumerELFRelocationReportsBoundedToolFailure(t *testing.T) {
	tool := relocationToolFixture(t, "#!/bin/sh\necho 'fixture patch refused' >&2\nexit 3\n")
	relocator := consumerELFRelocator{ctx: t.Context(), loader: "/nix/store/fixture-glibc/lib/ld.so", scratch: t.TempDir()}
	relocator.recipe.ToolPath = tool
	_, err := relocator.relocate("bin/program", dynamicELFFixture{machine: nativeELFMachine(), dynamic: true}.build())
	if err == nil || !strings.Contains(err.Error(), "fixture patch refused") {
		t.Fatalf("patch-tool failure lost its diagnostic: %v", err)
	}
	entries, err := os.ReadDir(relocator.scratch)
	testutil.FailErr(t, "inspect relocation scratch", err)
	if len(entries) != 0 {
		t.Fatal("failed relocation left scratch images behind")
	}
	var output relocationToolOutput
	n, err := output.Write([]byte("short"))
	if err != nil || n != 5 || string(output.data) != "short" {
		t.Fatal("short patch-tool diagnostic was not retained")
	}
}

func TestConsumerHostProvidersRequireNixStoreFiles(t *testing.T) {
	directory := t.TempDir()
	testutil.FailErr(t, "write ambient library fixture", os.WriteFile(filepath.Join(directory, "libambient.so"), []byte("library"), 0o600))
	relocator := consumerELFRelocator{members: map[string]onefile.Member{"libdata.so": {Data: []byte("not an image")}}}
	if relocator.dependencyPresent("program", "libambient.so", []string{directory, filepath.Join(directory, "missing")}) {
		t.Fatal("ambient host library admitted as a store provider")
	}
	if relocator.dependencyPresent("program", "libdata.so", []string{"$ORIGIN"}) {
		t.Fatal("non-image payload member admitted as a library provider")
	}
}
