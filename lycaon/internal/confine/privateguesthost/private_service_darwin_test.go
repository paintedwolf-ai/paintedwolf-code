package privateguesthost

import (
	"debug/macho"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateGuestAuthenticatesRunningImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service-fixture")
	fat, err := macho.OpenFat("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	defer fat.Close()
	cpu := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		cpu = macho.CpuAmd64
	}
	input, err := os.Open("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	for _, slice := range fat.Arches {
		if slice.Cpu == cpu {
			data, err := io.ReadAll(io.NewSectionReader(input, int64(slice.Offset), int64(slice.Size)))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0700); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", path).CombinedOutput(); err != nil {
		t.Fatalf("sign fixture: %v: %s", err, out)
	}
	output, err := exec.Command("/usr/bin/codesign", "-d", "--verbose=4", path).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect system fixture: %v: %s", err, output)
	}
	var cdhash string
	for line := range strings.Lines(string(output)) {
		if value, ok := strings.CutPrefix(line, "CDHash="); ok {
			cdhash = strings.TrimSpace(value)
			break
		}
	}
	if raw, err := hex.DecodeString(cdhash); err != nil || len(raw) != 20 {
		t.Fatalf("invalid fixture identity %q", cdhash)
	}
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fds := []*os.File{file, file, file, file}
	if _, err := spawnGuest(path, strings.Repeat("0", 40), fds); err == nil {
		t.Fatal("resumed an unrecognized image")
	}
	process, err := spawnGuest(path, cdhash, fds)
	if err != nil {
		t.Fatal(err)
	}
	state, err := process.Wait()
	if err != nil || !state.Success() {
		t.Fatalf("verified image: %v, %v", state, err)
	}
}
