//go:build linux

package confine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinuxDeniedFileMasksPreserveAllowedIO(t *testing.T) {
	self := requireLinuxSandbox(t)
	project := t.TempDir()
	first, second := filepath.Join(project, "first-secret"), filepath.Join(project, "second-secret")
	for _, path := range []string{first, second} {
		testutil.FailErr(t, "seed exact denied file", os.WriteFile(path, []byte("private-original"), 0o600))
	}
	box := denyConfinement(project)
	box.ReadDenyPaths = []string{first, second}
	status, output := linuxConfinedRun(t, self, box, nil, "/bin/sh", "-c", `
for path do
 if cat "$path"; then exit 41; fi
 if printf changed > "$path"; then exit 42; fi
done
printf allowed > "$1.allowed"
printf mask-applied
`, "file-mask-control", first, second)
	if status.ExitStatus() != 0 || !strings.Contains(output, "mask-applied") || strings.Contains(output, "private-original") {
		t.Fatalf("file masks did not preserve the applied boundary: status=%v output=%s", status, output)
	}
	for _, path := range []string{first, second} {
		data, err := os.ReadFile(path)
		testutil.FailErr(t, "inspect original denied file", err)
		if string(data) != "private-original" {
			t.Fatal("file mask changed the original host file")
		}
	}
	data, err := os.ReadFile(first + ".allowed")
	testutil.FailErr(t, "read allowed masked-command output", err)
	if string(data) != "allowed" {
		t.Fatal("file masking lost the allowed write")
	}
}
