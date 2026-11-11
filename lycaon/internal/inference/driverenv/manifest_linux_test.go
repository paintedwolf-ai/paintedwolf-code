//go:build linux

package driverenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNvidiaManifestPreservesDeclaredAPIVersion(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "set fixture root mode", os.Chmod(root, 0o700))
	directory := filepath.Join(root, "usr/share/vulkan/icd.d")
	testutil.FailErr(t, "create ICD directory", os.MkdirAll(directory, 0o755))
	path := filepath.Join(directory, "nvidia_icd.json")
	body := `{"file_format_version":"1.0.1","ICD":{"library_path":"libGLX_nvidia.so.0","api_version":"1.4.312","library_arch":"64"}}`
	testutil.FailErr(t, "write ICD fixture", os.WriteFile(path, []byte(body), 0o644))
	version, err := nvidiaAPIVersion(root, uint32(os.Getuid()))
	testutil.FailErr(t, "resolve NVIDIA API version", err)
	if version != "1.4.312" {
		t.Fatalf("API version = %q", version)
	}
	testutil.FailErr(t, "make manifest writable", os.Chmod(path, 0o666))
	if _, err := nvidiaAPIVersion(root, uint32(os.Getuid())); err == nil {
		t.Fatal("accepted writable manifest")
	}
	if _, err := nvidiaAPIVersion(t.TempDir(), uint32(os.Getuid())); err == nil {
		t.Fatal("accepted missing manifest")
	}
}

func TestNvidiaManifestRejectsMalformedVersions(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"file_format_version":"2.0.0","ICD":{"api_version":"1.4.312"}}`,
		`{"file_format_version":"1.0.1","ICD":{"api_version":"1.4"}}`,
		`{"file_format_version":"1.0.1","ICD":{"api_version":"128.4.312"}}`,
		`{"file_format_version":"1.0.1","ICD":{"api_version":"1.4.312"}} {}`,
		strings.Repeat(" ", 65537),
	} {
		if _, err := manifestAPIVersion([]byte(body)); err == nil {
			t.Fatalf("accepted invalid manifest %q", body[:min(len(body), 80)])
		}
	}
}
