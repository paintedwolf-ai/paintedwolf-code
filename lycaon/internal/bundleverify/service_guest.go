package bundleverify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const serviceGuestDir = "Contents/Resources/service-guest"

var codeDirectoryHash = regexp.MustCompile("^[a-f0-9]{40}\n$")

func checkServiceGuestAssets(app string) []Finding {
	base := filepath.Join(app, serviceGuestDir)
	var manifest struct {
		Schema       int    `json:"schema"`
		Architecture string `json:"architecture"`
		Kernel       string `json:"kernel_sha256"`
		Initramfs    string `json:"initramfs_sha256"`
	}
	content, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if err != nil || json.Unmarshal(content, &manifest) != nil || manifest.Schema != 1 || (manifest.Architecture != "arm64" && manifest.Architecture != "amd64") {
		return []Finding{guestAssetFinding("manifest.json", "invalid guest manifest")}
	}
	var findings []Finding
	for _, file := range []struct{ name, digest string }{{"kernel", manifest.Kernel}, {"initramfs.gz", manifest.Initramfs}} {
		if err := verifyImageDigest(filepath.Join(base, file.name), file.digest); err != nil {
			findings = append(findings, guestAssetFinding(file.name, err.Error()))
		}
	}
	data, err := os.ReadFile(filepath.Join(base, "pw-service-vm.cdhash"))
	if err != nil || !codeDirectoryHash.Match(data) {
		findings = append(findings, guestAssetFinding("pw-service-vm.cdhash", "invalid code directory hash"))
	}
	return findings
}

func verifyImageDigest(path, expected string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("missing guest image")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return fmt.Errorf("image digest mismatch")
	}
	return nil
}

func guestAssetFinding(name, reason string) Finding {
	return Finding{Code: CodeEngineResourceMissing, Severity: SeverityError, Path: filepath.Join(serviceGuestDir, name), Detail: map[string]string{"reason": reason}}
}
