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
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/platformfloor"
)

const serviceGuestDir = "Contents/Resources/service-guest"

var codeDirectoryHash = regexp.MustCompile("^[a-f0-9]{40}\n$")

func checkServiceGuestAssets(app string, images []MachOFacts) []Finding {
	base := filepath.Join(app, serviceGuestDir)
	var manifest struct {
		Schema       int    `json:"schema"`
		Architecture string `json:"architecture"`
		Kernel       string `json:"kernel_sha256"`
		Initramfs    string `json:"initramfs_sha256"`
	}
	content, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if err != nil || json.Unmarshal(content, &manifest) != nil || manifest.Schema != 1 || !slices.Contains(platformfloor.DarwinArches(), manifest.Architecture) {
		return []Finding{guestAssetFinding("manifest.json", "invalid guest manifest")}
	}
	var findings []Finding
	helper := slices.IndexFunc(images, func(image MachOFacts) bool { return image.Path == filepath.Join(app, serviceGuestHelper) })
	if helper < 0 || len(images[helper].Slices) != 1 || images[helper].Slices[0].GoArch != manifest.Architecture {
		findings = append(findings, guestAssetFinding("pw-service-vm", "helper must contain the single CPU architecture declared by the guest manifest"))
	}
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

func checkGuestHelperSignature(app string, display []byte) []Finding {
	values := map[string]string{}
	for line := range strings.Lines(string(display)) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && (key == "Identifier" || key == "CDHash") {
			values[key] = value
		}
	}
	if values["Identifier"] != "dev.paintedwolf.code.service-vm" {
		return []Finding{guestAssetFinding("pw-service-vm", "unexpected service helper signing identifier")}
	}
	data, err := os.ReadFile(filepath.Join(app, serviceGuestDir, "pw-service-vm.cdhash"))
	if err == nil && codeDirectoryHash.Match(data) && strings.TrimSpace(string(data)) != values["CDHash"] {
		return []Finding{guestAssetFinding("pw-service-vm.cdhash", "packaged code identity differs from the signed service helper")}
	}
	return nil
}
