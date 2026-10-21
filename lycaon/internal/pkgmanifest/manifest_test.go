package pkgmanifest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

// fixture writes one executable and a manifest describing it.
func fixture(t *testing.T, mutate func(m map[string]any)) (manifestPath, exePath string) {
	t.Helper()
	root := t.TempDir()
	exePath = filepath.Join(root, "bin", "pw")
	testutil.FailErr(t, "create bin directory", os.MkdirAll(filepath.Dir(exePath), 0o755))
	content := []byte("#!/bin/sh\necho painted wolf\n")
	testutil.FailErr(t, "write executable fixture", os.WriteFile(exePath, content, 0o755))
	sum := sha256.Sum256(content)
	m := map[string]any{
		"format_version": pkgmanifest.FormatVersion,
		"package":        "painted-wolf-code",
		"version":        "1.0.2",
		"architecture":   "x86_64",
		"install_root":   root,
		"executables": []map[string]any{
			{"path": exePath, "sha256": hex.EncodeToString(sum[:]), "size": len(content)},
		},
	}
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.Marshal(m)
	testutil.FailErr(t, "encode manifest fixture", err)
	manifestPath = filepath.Join(root, "manifest.json")
	testutil.FailErr(t, "write manifest fixture", os.WriteFile(manifestPath, raw, 0o644))
	return manifestPath, exePath
}

func executables(m map[string]any) []map[string]any {
	return m["executables"].([]map[string]any)
}

func TestLoadAcceptsAValidManifest(t *testing.T) {
	manifestPath, exePath := fixture(t, nil)
	m, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "load valid manifest", err)
	if m.Package != "painted-wolf-code" || m.Version != "1.0.2" || m.Architecture != "x86_64" {
		t.Fatalf("manifest = %+v", m)
	}
	exe, ok := m.Lookup(exePath)
	if !ok || exe.Path != exePath || exe.Size == 0 {
		t.Fatalf("Lookup(%q) = %+v, %v; want the listed executable", exePath, exe, ok)
	}
	if _, ok := m.Lookup(filepath.Join(filepath.Dir(exePath), "other")); ok {
		t.Fatal("Lookup found an unlisted path")
	}
	testutil.FailErr(t, "verify intact executable", m.Verify(exePath))
}

func TestLoadAcceptsPreReleaseVersionsAndEveryShippedArch(t *testing.T) {
	manifestPath, _ := fixture(t, func(m map[string]any) {
		m["version"] = "1.0.3-rc1"
		m["architecture"] = "aarch64"
	})
	_, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "load pre-release aarch64 manifest", err)
}

func TestLoadRefusesUnknownFields(t *testing.T) {
	manifestPath, _ := fixture(t, func(m map[string]any) { m["signature"] = "abc" })
	if _, err := pkgmanifest.Load(manifestPath); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("Load with an unknown field = %v, want a decode error naming the field", err)
	}
}

func TestLoadRefusesTrailingDataAndMissingFiles(t *testing.T) {
	manifestPath, _ := fixture(t, nil)
	raw, err := os.ReadFile(manifestPath)
	testutil.FailErr(t, "read manifest fixture", err)
	testutil.FailErr(t, "append trailing document", os.WriteFile(manifestPath, append(raw, []byte("{}")...), 0o644))
	if _, err := pkgmanifest.Load(manifestPath); err == nil {
		t.Fatal("Load accepted a manifest with trailing data")
	}
	if _, err := pkgmanifest.Load(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) = %v, want the os error", err)
	}
}

func TestLoadReportsAForeignFormatVersion(t *testing.T) {
	manifestPath, _ := fixture(t, func(m map[string]any) { m["format_version"] = 7 })
	_, err := pkgmanifest.Load(manifestPath)
	var unsupported *pkgmanifest.UnsupportedFormatError
	if !errors.As(err, &unsupported) || unsupported.Found != 7 {
		t.Fatalf("Load with format_version 7 = %v, want *UnsupportedFormatError{Found: 7}", err)
	}
}

func TestValidateRejectsEachBrokenField(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"empty package":      func(m map[string]any) { m["package"] = "" },
		"prose version":      func(m map[string]any) { m["version"] = "latest" },
		"unshipped arch":     func(m map[string]any) { m["architecture"] = "i386" },
		"relative root":      func(m map[string]any) { m["install_root"] = "opt/painted-wolf-code" },
		"unclean root":       func(m map[string]any) { m["install_root"] = "/opt//painted-wolf-code/" },
		"no executables":     func(m map[string]any) { m["executables"] = []map[string]any{} },
		"relative exe path":  func(m map[string]any) { executables(m)[0]["path"] = "bin/pw" },
		"unclean exe path":   func(m map[string]any) { executables(m)[0]["path"] = "/opt/./bin/pw" },
		"duplicate exe path": func(m map[string]any) { m["executables"] = append(executables(m), executables(m)[0]) },
		"short digest":       func(m map[string]any) { executables(m)[0]["sha256"] = "abc" },
		"uppercase digest": func(m map[string]any) {
			executables(m)[0]["sha256"] = strings.ToUpper(executables(m)[0]["sha256"].(string))
		},
		"non-hex digest": func(m map[string]any) { executables(m)[0]["sha256"] = strings.Repeat("g", 64) },
		"negative size":  func(m map[string]any) { executables(m)[0]["size"] = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifestPath, _ := fixture(t, mutate)
			if m, err := pkgmanifest.Load(manifestPath); err == nil {
				t.Fatalf("Load accepted %s: %+v", name, m)
			}
		})
	}
}

func TestVerifyReportsTamperingAndUnlistedPaths(t *testing.T) {
	manifestPath, exePath := fixture(t, nil)
	m, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "load manifest", err)

	image, err := os.ReadFile(exePath)
	testutil.FailErr(t, "read executable fixture", err)
	image[0] ^= 1
	testutil.FailErr(t, "tamper with same-size executable", os.WriteFile(exePath, image, 0o755))
	var mismatch *pkgmanifest.MismatchError
	if err := m.Verify(exePath); !errors.As(err, &mismatch) {
		t.Fatalf("Verify(tampered) = %v, want *MismatchError", err)
	}
	if mismatch.Path != exePath || mismatch.Want == mismatch.Got || len(mismatch.Got) != 64 {
		t.Fatalf("mismatch = %+v", mismatch)
	}

	var unlisted *pkgmanifest.UnlistedError
	other := filepath.Join(filepath.Dir(exePath), "other")
	if err := m.Verify(other); !errors.As(err, &unlisted) || unlisted.Path != other {
		t.Fatalf("Verify(unlisted) = %v, want *UnlistedError for %q", err, other)
	}

	testutil.FailErr(t, "remove executable", os.Remove(exePath))
	if err := m.Verify(exePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Verify(removed) = %v, want the os error", err)
	}
}

func TestVerifyReportsWrongRecordedSize(t *testing.T) {
	manifestPath, exePath := fixture(t, func(m map[string]any) { executables(m)[0]["size"] = 1 })
	m, err := pkgmanifest.Load(manifestPath)
	testutil.FailErr(t, "load manifest", err)
	var mismatch *pkgmanifest.SizeMismatchError
	if err := m.Verify(exePath); !errors.As(err, &mismatch) {
		t.Fatalf("Verify with a wrong recorded size = %v, want *SizeMismatchError", err)
	}
}
