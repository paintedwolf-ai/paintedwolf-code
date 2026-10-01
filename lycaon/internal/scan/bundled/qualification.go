package bundled

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/platformfloor"
)

func verifyQualification(m *Manifest, lock candidateSourceLock, proof *sourceProof) error {
	expected, err := expectedContractCases(lock, proof)
	if err != nil {
		return err
	}
	if err := verifyContractReport(filepath.Join(m.artifactDirectory, "contracts.jsonl"), expected); err != nil {
		return err
	}
	return verifyPlatformReport(m, proof)
}

func sourceDocument(proof *sourceProof, name string, value any) error {
	raw, ok := proof.documents["inputs/"+name]
	if !ok {
		return fmt.Errorf("locked qualification input missing: %s", name)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("invalid qualification input %s: %w", name, err)
	}
	return nil
}

func expectedContractCases(lock candidateSourceLock, proof *sourceProof) (map[string]bool, error) {
	var translations []string
	if err := sourceDocument(proof, "source/tests/rule-translation.json", &translations); err != nil {
		return nil, err
	}
	expected := make(map[string]bool)
	add := func(name string) error {
		if name == "" || expected[name] {
			return fmt.Errorf("duplicate or empty frozen contract: %s", name)
		}
		expected[name] = true
		return nil
	}
	for config := range lock.Files {
		if !strings.HasPrefix(config, "source/tests/tainting/") || path.Ext(config) != ".yaml" {
			continue
		}
		prefix := strings.TrimSuffix(config, ".yaml") + "."
		count := 0
		for source := range lock.Files {
			if !strings.HasPrefix(source, prefix) || path.Dir(source) != path.Dir(config) || path.Ext(source) == ".yaml" || path.Ext(source) == ".json" {
				continue
			}
			count++
			if err := add(source); err != nil {
				return nil, err
			}
			if slices.Contains(translations, strings.TrimPrefix(config, "source/tests/")) {
				if err := add("rule-translation/" + strings.TrimPrefix(source, "source/tests/")); err != nil {
					return nil, err
				}
			}
		}
		if count == 0 {
			return nil, fmt.Errorf("frozen contract has no source: %s", config)
		}
	}
	var selections []struct {
		Language string `json:"language"`
	}
	if err := sourceDocument(proof, "source/tests/native-file-selection.json", &selections); err != nil {
		return nil, err
	}
	for _, item := range selections {
		if item.Language == "" {
			return nil, fmt.Errorf("file-selection language missing")
		}
		if err := add("file-selection/" + item.Language); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"callback-rule-validation", "model-rule-validation", "native-pattern-validation"} {
		var validations []struct {
			Name string `json:"name"`
		}
		if err := sourceDocument(proof, "source/tests/"+name+".json", &validations); err != nil {
			return nil, err
		}
		for _, item := range validations {
			if item.Name == "" {
				return nil, fmt.Errorf("rule-validation name missing")
			}
			if err := add("rule-validation/" + item.Name); err != nil {
				return nil, err
			}
		}
	}
	if len(expected) == 0 {
		return nil, fmt.Errorf("frozen contract inventory is empty")
	}
	return expected, nil
}

type platformImage struct {
	Path               string   `json:"path"`
	SHA256             string   `json:"sha256"`
	Architectures      []string `json:"architectures"`
	DeploymentVersions [][]int  `json:"deployment_versions"`
	Dependencies       []string `json:"dependencies"`
	RPaths             []string `json:"rpaths"`
}
type platformReport struct {
	DeploymentTarget string          `json:"deployment_target"`
	Images           []platformImage `json:"images"`
}

func verifyPlatformReport(m *Manifest, proof *sourceProof) error {
	if m.identity.GOOS == "linux" {
		return verifyLinuxPlatformReport(m, proof)
	}
	if m.identity.GOOS != "darwin" {
		return fmt.Errorf("no maintained platform qualification validator for %s", m.identity.GOOS)
	}
	var checks platformReport
	raw, err := readCandidateMetadata(filepath.Join(m.artifactDirectory, "platform-checks.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &checks); err != nil {
		return err
	}
	var runtime struct {
		Environment map[string]string `json:"environment"`
	}
	raw, err = readCandidateMetadata(filepath.Join(m.artifactDirectory, "runtime.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &runtime); err != nil {
		return err
	}
	var runtimes struct {
		MacOS struct {
			DeploymentTarget string `json:"deployment_target"`
		} `json:"macos"`
	}
	if err := sourceDocument(proof, "locks/runtimes.json", &runtimes); err != nil {
		return err
	}
	if checks.DeploymentTarget != runtime.Environment["MACOSX_DEPLOYMENT_TARGET"] || checks.DeploymentTarget != runtimes.MacOS.DeploymentTarget {
		return fmt.Errorf("platform deployment target differs between qualification records")
	}
	major, minor, err := platformfloor.MacOSMinParts()
	if err != nil {
		return err
	}
	if !supportedDeploymentTarget(checks.DeploymentTarget, major, minor) {
		return fmt.Errorf("platform deployment target %q is invalid or exceeds supported macOS %s", checks.DeploymentTarget, platformfloor.MacOSMin())
	}
	seen := make(map[string]bool)
	for _, image := range checks.Images {
		if !validArchivePath(image.Path) || seen[image.Path] || !validHex(image.SHA256, 32) {
			return fmt.Errorf("invalid or duplicate platform image: %s", image.Path)
		}
		seen[image.Path] = true
		if !slices.Contains(image.Architectures, candidateArchitecture("darwin", m.identity.GOARCH)) {
			return fmt.Errorf("platform image architecture differs: %s", image.Path)
		}
		if len(image.DeploymentVersions) == 0 {
			return fmt.Errorf("platform image minimum OS missing: %s", image.Path)
		}
		for _, version := range image.DeploymentVersions {
			if !supportedImageVersion(version, major, minor) {
				return fmt.Errorf("platform image requires unsupported minimum OS: %s", image.Path)
			}
		}
		for _, value := range append(slices.Clone(image.Dependencies), image.RPaths...) {
			if strings.HasPrefix(value, "/") && (path.Clean(value) != value || (!strings.HasPrefix(value, "/usr/lib/") && !strings.HasPrefix(value, "/System/Library/"))) {
				return fmt.Errorf("platform image has non-system absolute dependency: %s", image.Path)
			}
		}
		if image.Path == "opengrep" && image.SHA256 != m.identity.BinarySHA256 {
			return fmt.Errorf("platform checks bind a different executable")
		}
	}
	if !seen["opengrep"] {
		return fmt.Errorf("platform executable check missing")
	}
	return nil
}

func supportedDeploymentTarget(target string, major, minor int) bool {
	fields := strings.Split(target, ".")
	version := make([]int, len(fields))
	for i, field := range fields {
		if field == "" || strings.ContainsAny(field, "+-") {
			return false
		}
		value, err := strconv.Atoi(field)
		if err != nil {
			return false
		}
		version[i] = value
	}
	return supportedImageVersion(version, major, minor)
}

func supportedImageVersion(version []int, major, minor int) bool {
	if len(version) < 2 || len(version) > 3 {
		return false
	}
	for _, part := range version {
		if part < 0 {
			return false
		}
	}
	if version[0] != major {
		return version[0] < major
	}
	if version[1] != minor {
		return version[1] < minor
	}
	return len(version) == 2 || version[2] == 0
}
