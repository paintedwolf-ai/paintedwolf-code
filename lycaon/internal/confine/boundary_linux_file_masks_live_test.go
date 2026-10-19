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
	for _, mode := range []string{"writable", "read-only temporary root"} {
		t.Run(mode, func(t *testing.T) {
			project := t.TempDir()
			first, second := filepath.Join(project, "first-secret"), filepath.Join(project, "second-secret")
			for _, path := range []string{first, second} {
				testutil.FailErr(t, "seed exact denied file", os.WriteFile(path, []byte("private-original"), 0o600))
			}
			allowed := filepath.Join(project, "allowed")
			testutil.FailErr(t, "seed allowed input", os.WriteFile(allowed, []byte("allowed-input"), 0o600))
			box := denyConfinement(project)
			box.ReadDenyPaths = []string{first, second}
			if mode != "writable" {
				box.ReadRoots = []string{"/tmp"}
			}
			status, output := linuxConfinedRun(t, self, box, nil, "/bin/sh", "-c", `
for path in "$1" "$2"; do
 if cat "$path"; then exit 41; fi
 if printf changed > "$path"; then exit 42; fi
done
cat "$3" || exit 43
if [ "$4" = writable ]; then printf written > "$3.out" || exit 44; fi
printf mask-applied
`, "file-mask-control", first, second, allowed, mode)
			if status.ExitStatus() != 0 || !strings.Contains(output, "mask-applied") || !strings.Contains(output, "allowed-input") || strings.Contains(output, "private-original") {
				t.Fatalf("file masks did not preserve the applied boundary: status=%v output=%s", status, output)
			}
			for _, path := range []string{first, second} {
				data, err := os.ReadFile(path)
				testutil.FailErr(t, "inspect original denied file", err)
				if string(data) != "private-original" {
					t.Fatal("file mask changed the original host file")
				}
			}
			if mode == "writable" {
				data, err := os.ReadFile(allowed + ".out")
				testutil.FailErr(t, "read allowed masked-command output", err)
				if string(data) != "written" {
					t.Fatal("file masking lost the allowed write")
				}
			}
		})
	}
}
