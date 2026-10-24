//go:build darwin || linux

package pkgmanifest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestSharedPackagingProducerManifestContract(t *testing.T) {
	corpus := filepath.Join(testutil.CheckoutRoot(t), "packaging/linux/manifest/testdata")
	for _, arch := range []string{"x86_64", "aarch64"} {
		t.Run(arch, func(t *testing.T) {
			raw := contractRead(t, filepath.Join(corpus, arch+".json"))
			manifest, err := pkgmanifest.Read(bytes.NewReader(raw))
			if err != nil {
				testutil.FailErr(t, "read real producer manifest", err)
			}
			root := t.TempDir()
			elf := contractRead(t, filepath.Join(corpus, arch+".elf"))
			for _, entry := range manifest.Payload {
				path := filepath.Join(root, entry.Path)
				switch entry.Kind {
				case "directory":
					err = os.MkdirAll(path, os.FileMode(entry.Mode))
				case "symlink":
					err = os.Symlink(*entry.Target, path)
					if err == nil {
						info, statErr := os.Lstat(path)
						if statErr != nil {
							testutil.FailErr(t, "stat shared symlink", statErr)
						}
						// Darwin applies the umask to symlinks; materialize the sealed Linux mode
						// without following the link or changing the production verifier.
						if uint32(info.Mode().Perm()) != entry.Mode {
							err = unix.Fchmodat(unix.AT_FDCWD, path, entry.Mode, unix.AT_SYMLINK_NOFOLLOW)
						}
					}
				case "file":
					data := elf
					if !strings.HasPrefix(entry.Path, "bin/") {
						data = contractRead(t, filepath.Join(corpus, "payload", entry.Path))
					}
					err = os.WriteFile(path, data, os.FileMode(entry.Mode))
				}
				if err != nil {
					testutil.FailErr(t, "materialize shared payload", err)
				}
			}
			retained, err := os.OpenRoot(root)
			if err != nil {
				testutil.FailErr(t, "retain payload root", err)
			}
			defer retained.Close()
			if err = manifest.VerifyPayload(retained); err != nil {
				testutil.FailErr(t, "verify producer payload", err)
			}
			for _, entry := range manifest.Executables {
				data := elf
				if strings.HasPrefix(entry.Path, "/usr/bin/") {
					data = contractRead(t, filepath.Join(corpus, "launcher.sh"))
				}
				file, err := os.CreateTemp(t.TempDir(), "executable")
				if err != nil {
					testutil.FailErr(t, "create executable", err)
				}
				if _, err = file.Write(data); err != nil {
					testutil.FailErr(t, "write executable", err)
				}
				if err = pkgmanifest.VerifyOpened(file, entry); err != nil {
					testutil.FailErr(t, "verify producer executable", err)
				}
				if _, err = file.WriteAt([]byte("changed"), 0); err != nil {
					testutil.FailErr(t, "mutate executable", err)
				}
				if pkgmanifest.VerifyOpened(file, entry) == nil {
					t.Fatal("accepted changed executable bytes")
				}
				file.Close()
			}
			for _, mutation := range []string{"unknown version", "missing seal"} {
				var document map[string]any
				if err = json.Unmarshal(raw, &document); err != nil {
					testutil.FailErr(t, "decode fixture for mutation", err)
				}
				if mutation == "unknown version" {
					document["format_version"] = 99
				} else {
					delete(document, "payload")
				}
				bad, err := json.Marshal(document)
				if err != nil {
					testutil.FailErr(t, "marshal mutation", err)
				}
				if _, err = pkgmanifest.Read(bytes.NewReader(bad)); err == nil {
					t.Fatalf("accepted %s", mutation)
				}
			}
		})
	}
}

func contractRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		testutil.FailErr(t, "read shared producer corpus", err)
	}
	return raw
}
