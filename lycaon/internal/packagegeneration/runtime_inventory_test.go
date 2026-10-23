package packagegeneration

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func moduleInventoryFixture() *RuntimeInventory {
	return &RuntimeInventory{Architecture: "x86_64", Guard: "/runtime/guard.so",
		GuardWitnessOffset: 16000, GuardInventoryOffset: 17000,
		Entries: []RuntimeModule{{Path: "/runtime/z.so", Size: 6, SHA256: runtimeDigest([]byte("module"))},
			{Path: "/runtime/é.so", Size: 3, SHA256: runtimeDigest([]byte("elf"))},
			{Path: "/runtime/a.so", Size: 3, SHA256: runtimeDigest([]byte("elf"))}}}
}

func pythonInventory(t *testing.T, source string, input []byte) []byte {
	t.Helper()
	command := exec.CommandContext(t.Context(), "python3", "-c", source)
	command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(testutil.CheckoutRoot(t), "scripts"))
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	if err != nil {
		t.Log(stderr.String())
		testutil.FailErr(t, "actual Python inventory codec", err)
	}
	return raw
}

func TestRuntimeInventoryActualPythonProducerAndConsumer(t *testing.T) {
	for _, architecture := range []string{"x86_64", "aarch64"} {
		t.Run(architecture, func(t *testing.T) {
			inventory := moduleInventoryFixture()
			inventory.Architecture = architecture
			request, err := json.Marshal(inventory)
			testutil.FailErr(t, "encode producer request", err)
			pythonBytes := pythonInventory(t, `import json,sys
from linux_release.module_integrity.inventory import encode
v=json.load(sys.stdin)
sys.stdout.buffer.write(encode(v["entries"],v["guard"],v["guard_witness_offset"],v["guard_inventory_offset"],v["architecture"]))
`, request)
			decoded, err := DecodeRuntimeInventory(pythonBytes)
			testutil.FailErr(t, "Go consumes actual Python bytes", err)
			goBytes, err := EncodeRuntimeInventory(inventory)
			testutil.FailErr(t, "Go producer", err)
			if !bytes.Equal(goBytes, pythonBytes) || decoded.Architecture != architecture {
				t.Fatal("actual producers disagree on canonical bytes")
			}
			response := pythonInventory(t, `import json,sys
from linux_release.module_integrity.inventory import decode
json.dump(decode(sys.stdin.buffer.read()),sys.stdout)
`, goBytes)
			var consumed struct {
				FormatVersion int    `json:"format_version"`
				SHA256        string `json:"sha256"`
				RuntimeInventory
			}
			testutil.FailErr(t, "read actual Python consumer", json.Unmarshal(response, &consumed))
			roundtrip, err := EncodeRuntimeInventory(&consumed.RuntimeInventory)
			testutil.FailErr(t, "reencode actual consumer result", err)
			if consumed.FormatVersion != 1 || consumed.SHA256 != runtimeDigest(goBytes) || !bytes.Equal(roundtrip, goBytes) {
				t.Fatal("actual Python decoder lost exact inventory contract")
			}
		})
	}
}

func TestRuntimeInventoryRefusesMalformedBytes(t *testing.T) {
	raw, err := EncodeRuntimeInventory(moduleInventoryFixture())
	testutil.FailErr(t, "encode fixture", err)
	cases := map[string][]byte{"short": raw[:8], "truncated": raw[:len(raw)-1], "trailing": append(bytes.Clone(raw), 'x')}
	for name, offset := range map[string]int{"magic": 0, "size": 8, "count": 12, "padding": 36, "machine": 34} {
		candidate := bytes.Clone(raw)
		candidate[offset] ^= 1
		cases[name] = candidate
	}
	unsorted := bytes.Clone(raw)
	first := 40 + len(moduleInventoryFixture().Guard)
	name := first + runtimeInventoryEntry
	unsorted[name+len("/runtime/")] = 'z'
	cases["duplicate or unsorted aliases"] = unsorted
	invalidUTF8 := bytes.Clone(raw)
	invalidUTF8[name] = 0xff
	cases["invalid UTF8"] = invalidUTF8
	badSize := bytes.Clone(raw)
	binary.LittleEndian.PutUint64(badSize[first:], 1<<63)
	cases["overflowing size"] = badSize
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRuntimeInventory(candidate); err == nil {
				t.Fatal("malformed inventory accepted")
			}
		})
	}
}

func TestRuntimeInventoryRefusesUnselectedAndAmbiguousProducerInputs(t *testing.T) {
	for name, mutate := range map[string]func(*RuntimeInventory){
		"guard as ordinary":        func(v *RuntimeInventory) { v.Entries[0].Path = v.Guard },
		"duplicate":                func(v *RuntimeInventory) { v.Entries[0] = v.Entries[1] },
		"zero bytes":               func(v *RuntimeInventory) { v.Entries[0].Size = 0 },
		"uppercase hash":           func(v *RuntimeInventory) { v.Entries[0].SHA256 = strings.Repeat("A", 64) },
		"relative":                 func(v *RuntimeInventory) { v.Entries[0].Path = "module.so" },
		"escape":                   func(v *RuntimeInventory) { v.Entries[0].Path = "/runtime/../module.so" },
		"unaligned witness":        func(v *RuntimeInventory) { v.GuardWitnessOffset++ },
		"unaligned digest":         func(v *RuntimeInventory) { v.GuardInventoryOffset++ },
		"oversized offset":         func(v *RuntimeInventory) { v.GuardInventoryOffset = runtimeGuardOffsetLimit + 8 },
		"unsupported architecture": func(v *RuntimeInventory) { v.Architecture = "other" },
		"empty":                    func(v *RuntimeInventory) { v.Entries = nil },
	} {
		t.Run(name, func(t *testing.T) {
			inventory := moduleInventoryFixture()
			mutate(inventory)
			if _, err := EncodeRuntimeInventory(inventory); err == nil {
				t.Fatal("ambiguous inventory accepted")
			}
		})
	}
}
