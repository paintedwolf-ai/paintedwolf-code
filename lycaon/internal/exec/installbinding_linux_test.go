//go:build linux

package exec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/installbinding"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLaunchesFromAReplacedInstallationAreRefused(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	engine, helper := filepath.Join(bin, "pw"), filepath.Join(bin, "helper")
	write := func(path, content string) {
		if err := os.WriteFile(path+".new", []byte(content), 0o755); err != nil {
			testutil.FailErr(t, "write file", err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			testutil.FailErr(t, "os.Rename failed", err)
		}
	}
	write(engine, "engine")
	write(helper, "#!/bin/sh\nexit 0\n")
	var executables []pkgmanifest.Executable
	payload := []pkgmanifest.PayloadEntry{{Path: "bin", Kind: "directory", Mode: 0o755}}
	for _, path := range []string{engine, helper} {
		raw, _ := os.ReadFile(path)
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		size := int64(len(raw))
		rel, _ := filepath.Rel(root, path)
		executables = append(executables, pkgmanifest.Executable{Path: path, SHA256: digest, Size: size})
		payload = append(payload, pkgmanifest.PayloadEntry{Path: filepath.ToSlash(rel), Kind: "file", Mode: 0o755, SHA256: &digest, Size: &size})
	}
	raw, err := json.Marshal(pkgmanifest.Manifest{FormatVersion: pkgmanifest.FormatVersion, Package: "painted-wolf-code",
		Version: "1.0.2", Architecture: "x86_64", InstallRoot: root, Executables: executables, Payload: payload})
	testutil.FailErr(t, "json.Marshal failed", err)
	manifest := filepath.Join(root, "manifest.json")
	write(manifest, string(raw))
	image, err := os.Open(engine)
	testutil.FailErr(t, "os.Open failed", err)
	defer func() { _ = image.Close() }()
	binding, err := installbinding.Capture(root, manifest, engine, image)
	testutil.FailErr(t, "installbinding.Capture failed", err)
	installbinding.Install(binding)
	t.Cleanup(func() { installbinding.Install(nil) })

	_, cleanup, err := PrepareCommand(t.Context(), helper, nil, ExecOpts{Launch: HostLaunch("installed helper")})
	if err != nil {
		t.Fatalf("unchanged helper refused: %v", err)
	}
	cleanup()
	write(helper, "#!/bin/sh\nexit 1\n")
	if _, _, err := PrepareCommand(t.Context(), helper, nil, ExecOpts{Launch: HostLaunch("installed helper")}); !errors.Is(err, installbinding.ErrReplaced) {
		t.Fatalf("replaced helper launch = %v, want restart outcome", err)
	}
	if err := StartReaper(helper); !errors.Is(err, installbinding.ErrReplaced) {
		t.Fatalf("replaced reaper launch = %v, want restart outcome", err)
	}
}
