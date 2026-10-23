//go:build linux

package packagegeneration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Exercise the Go producer against the actual linked anchors and Python byte
// preparation. Metadata-only ELF fixtures cannot prove this cross-language ABI.
func TestRuntimeInventoryActualNativeAnchorBinding(t *testing.T) {
	repository := testutil.CheckoutRoot(t)
	work := t.TempDir()
	run := func(name string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), name, args...)
		command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(repository, "scripts"))
		output, err := command.CombinedOutput()
		if err != nil {
			t.Log(string(output))
			t.Fatal(err)
		}
		return output
	}
	archive := filepath.Join(work, "digest", "target", "release", "libpw_module_digest.a")
	run("python3", "-m", "linux_release.module_integrity.abi", "--build-digest", "--output", filepath.Join(work, "digest"))
	source := filepath.Join(repository, "packaging", "linux", "module-audit")
	auditor, guard := filepath.Join(work, "audit.so"), filepath.Join(work, "guard.so")
	run("cc", "-shared", "-fPIC", "-nostdlib", "-ffreestanding", "-fno-builtin", "-fno-stack-protector", "-O2", filepath.Join(source, "audit.c"), archive, "-Wl,--exclude-libs,ALL,-Bsymbolic", "-o", auditor)
	run("cc", "-shared", "-fPIC", "-nostdlib", "-ffreestanding", "-fno-builtin", "-fno-stack-protector", "-O2", "-Wl,-z,initfirst,-z,relro,-z,now", filepath.Join(source, "guard.c"), "-o", guard)
	originalAudit, err := os.ReadFile(auditor)
	if err != nil {
		t.Fatal(err)
	}
	originalGuard, err := os.ReadFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	abi, err := InspectRuntimeModuleABI(originalAudit, originalGuard)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := EncodeRuntimeInventory(&RuntimeInventory{Architecture: abi.Architecture, Guard: "/runtime/guard.so", GuardWitnessOffset: abi.GuardWitnessOffset, GuardInventoryOffset: abi.GuardInventoryOffset, Entries: []RuntimeModule{{Path: "/usr/lib/module.so", Size: 3, SHA256: runtimeDigest([]byte("elf"))}}})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindRuntimeModules(originalAudit, originalGuard, inventory)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(work, "inventory.bin")
	if err := os.WriteFile(input, inventory, 0600); err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(work, "prepared")
	if err := os.Mkdir(prepared, 0700); err != nil {
		t.Fatal(err)
	}
	report := run("python3", "-c", `import json,sys
from pathlib import Path
from linux_release.module_integrity.binding import bind
print(json.dumps(bind(Path(sys.argv[1]),Path(sys.argv[2]),Path(sys.argv[3]).read_bytes(),Path(sys.argv[4]))))
`, auditor, guard, input, prepared)
	var result struct {
		InventorySHA256 string           `json:"inventory_sha256"`
		FinalABI        RuntimeModuleABI `json:"final_abi"`
	}
	if err := json.Unmarshal(report, &result); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string][]byte{"pw-module-audit.so": bound.Auditor, "pw-module-guard.so": bound.Guard, "module-inventory.bin": bound.Inventory} {
		actual, err := os.ReadFile(filepath.Join(prepared, name))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("actual preparation differs for %s: %v", name, err)
		}
	}
	if result.InventorySHA256 != bound.InventorySHA256 || result.FinalABI.GuardSHA256 != bound.FinalABI.GuardSHA256 || result.FinalABI.AuditorSHA256 != bound.FinalABI.AuditorSHA256 {
		t.Fatal("actual producer identities disagree")
	}
	auditAfter, _ := os.ReadFile(auditor)
	guardAfter, _ := os.ReadFile(guard)
	if !bytes.Equal(originalAudit, auditAfter) || !bytes.Equal(originalGuard, guardAfter) {
		t.Fatal("binding changed original inputs")
	}
	if _, err := BindRuntimeModules(bound.Auditor, bound.Guard, inventory); err == nil {
		t.Fatal("accepted previously bound anchors")
	}
}
