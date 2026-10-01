package bundleverify

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Synthetic Mach-O fixtures are independent of the host toolchain.

const (
	machoMagic64 = 0xfeedfacf
	machoFatples = 0xcafebabe
	fileTypeExec = 2

	cmdLoadDylib = 0x0c
)

func cpuTypeFor(cpu macho.Cpu) uint32 { return uint32(cpu) }

// packVersion renders "13.0" as the xxxx.yy.zz word Mach-O stores.
func packVersion(t *testing.T, v string) uint32 {
	t.Helper()
	if v == "" {
		return 0
	}
	fields := strings.Split(v, ".")
	major, err := strconv.Atoi(fields[0])
	if err != nil {
		testutil.FailErr(t, "parse fixture version major", err)
	}
	var minor int
	if len(fields) > 1 {
		if minor, err = strconv.Atoi(fields[1]); err != nil {
			testutil.FailErr(t, "parse fixture version minor", err)
		}
	}
	return uint32(major)<<16 | uint32(minor)<<8
}

// buildThinMachO returns the bytes of a 64-bit Mach-O carrying one
// LC_BUILD_VERSION (when minOS is non-empty) and one LC_LOAD_DYLIB per entry.
func buildThinMachO(t *testing.T, cpu macho.Cpu, minOS string, dylibs []string, extra []byte) []byte {
	t.Helper()

	var cmds bytes.Buffer
	ncmds := uint32(0)

	if minOS != "" {
		// cmd, cmdsize, platform, minos, sdk, ntools
		writeU32(&cmds, 0x32)
		writeU32(&cmds, 24)
		writeU32(&cmds, 1) // PLATFORM_MACOS
		writeU32(&cmds, packVersion(t, minOS))
		writeU32(&cmds, packVersion(t, minOS))
		writeU32(&cmds, 0)
		ncmds++
	}

	for _, name := range dylibs {
		nameBytes := append([]byte(name), 0)
		for len(nameBytes)%4 != 0 {
			nameBytes = append(nameBytes, 0)
		}
		cmdsize := uint32(24 + len(nameBytes))
		writeU32(&cmds, cmdLoadDylib)
		writeU32(&cmds, cmdsize)
		writeU32(&cmds, 24) // name offset
		writeU32(&cmds, 0)  // timestamp
		writeU32(&cmds, 0x10000)
		writeU32(&cmds, 0x10000)
		cmds.Write(nameBytes)
		ncmds++
	}

	var out bytes.Buffer
	writeU32(&out, machoMagic64)
	writeU32(&out, cpuTypeFor(cpu))
	writeU32(&out, 0) // cpusubtype
	writeU32(&out, fileTypeExec)
	writeU32(&out, ncmds)
	writeU32(&out, uint32(cmds.Len()))
	writeU32(&out, 0) // flags
	writeU32(&out, 0) // reserved
	out.Write(cmds.Bytes())
	out.Write(extra)

	return out.Bytes()
}

func writeU32(w *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.Write(b[:])
}

func writeThinMachO(t *testing.T, path string, cpu macho.Cpu, minOS string, dylibs []string) string {
	t.Helper()
	return writeThinMachOWithExtra(t, path, cpu, minOS, dylibs, nil)
}

func writeThinMachOWithExtra(t *testing.T, path string, cpu macho.Cpu, minOS string, dylibs []string, extra []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "mkdir fixture dir", err)
	}
	if err := os.WriteFile(path, buildThinMachO(t, cpu, minOS, dylibs, extra), 0o755); err != nil {
		testutil.FailErr(t, "write thin mach-o", err)
	}
	return path
}

type fatSlice struct {
	cpu    macho.Cpu
	minOS  string
	dylibs []string
}

// writeFatMachO writes a universal binary with one embedded thin Mach-O per
// slice, each aligned to a 4096-byte boundary.
func writeFatMachO(t *testing.T, path string, slices []fatSlice) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "mkdir fixture dir", err)
	}

	const align = 4096
	bodies := make([][]byte, 0, len(slices))
	for _, s := range slices {
		bodies = append(bodies, buildThinMachO(t, s.cpu, s.minOS, s.dylibs, nil))
	}

	headerLen := 8 + 20*len(slices)
	offsets := make([]uint32, len(slices))
	next := uint32(headerLen)
	for i, body := range bodies {
		if next%align != 0 {
			next += align - next%align
		}
		offsets[i] = next
		next += uint32(len(body))
	}

	// The fat header is big-endian by definition.
	var out bytes.Buffer
	writeU32BE(&out, machoFatples)
	writeU32BE(&out, uint32(len(slices)))
	for i, s := range slices {
		writeU32BE(&out, cpuTypeFor(s.cpu))
		writeU32BE(&out, 0) // cpusubtype
		writeU32BE(&out, offsets[i])
		writeU32BE(&out, uint32(len(bodies[i])))
		writeU32BE(&out, 12) // align 2^12
	}
	for i, body := range bodies {
		for uint32(out.Len()) < offsets[i] {
			out.WriteByte(0)
		}
		out.Write(body)
	}

	if err := os.WriteFile(path, out.Bytes(), 0o755); err != nil {
		testutil.FailErr(t, "write fat mach-o", err)
	}
	return path
}

func writeU32BE(w *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	w.Write(b[:])
}

func TestFixtureThinMachOParsesBack(t *testing.T) {
	path := writeThinMachO(t, filepath.Join(t.TempDir(), "thin"), macho.CpuArm64, "13.0", []string{"/usr/lib/libSystem.B.dylib"})

	facts, err := InspectMachO(path)
	if err != nil {
		testutil.FailErr(t, "inspect thin fixture", err)
	}
	if facts == nil {
		t.Fatal("thin fixture was not recognised as a Mach-O")
	}
	if facts.Fat {
		t.Fatal("thin fixture reported as fat")
	}
	if len(facts.Slices) != 1 {
		t.Fatalf("thin fixture has %d slices, want 1", len(facts.Slices))
	}
	got := facts.Slices[0]
	if got.GoArch != "arm64" {
		t.Fatalf("GoArch = %q, want arm64", got.GoArch)
	}
	if got.MinOS != "13.0" {
		t.Fatalf("MinOS = %q, want 13.0", got.MinOS)
	}
	if len(got.Dylibs) != 1 || got.Dylibs[0] != "/usr/lib/libSystem.B.dylib" {
		t.Fatalf("Dylibs = %v, want [/usr/lib/libSystem.B.dylib]", got.Dylibs)
	}
}

func TestFixtureFatMachOParsesBack(t *testing.T) {
	path := writeFatMachO(t, filepath.Join(t.TempDir(), "fat"), []fatSlice{
		{cpu: macho.CpuArm64, minOS: "13.0"},
		{cpu: macho.CpuAmd64, minOS: "13.0"},
	})

	facts, err := InspectMachO(path)
	if err != nil {
		testutil.FailErr(t, "inspect fat fixture", err)
	}
	if facts == nil {
		t.Fatal("fat fixture was not recognised as a Mach-O")
	}
	if !facts.Fat {
		t.Fatal("fat fixture not reported as fat")
	}
	if len(facts.Slices) != 2 {
		t.Fatalf("fat fixture has %d slices, want 2", len(facts.Slices))
	}
}

func TestInspectMachOSkipsNonMachO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "icudtl.dat")
	if err := os.WriteFile(path, []byte("not a mach-o, just data\x00\x01\x02"), 0o644); err != nil {
		testutil.FailErr(t, "write non-mach-o fixture", err)
	}

	facts, err := InspectMachO(path)
	if err != nil {
		testutil.FailErr(t, "inspect non-mach-o", err)
	}
	if facts != nil {
		t.Fatalf("non-Mach-O file was parsed as one: %+v", facts)
	}
}
