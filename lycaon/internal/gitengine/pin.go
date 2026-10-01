package gitengine

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

type pinFile struct {
	GitVersion string                 `yaml:"git_version"`
	LFSVersion string                 `yaml:"lfs_version"`
	Platforms  map[string]platformPin `yaml:"platforms"`
}

type platformPin struct {
	URL             string `yaml:"url"`
	Build           string `yaml:"build"`
	ReportedVersion string `yaml:"reported_version"`
	SHA256          string `yaml:"sha256"`
}

var (
	pinOnce        sync.Once
	pinGit         string
	pinReportedGit string
	pinErr         error
)

// PinnedVersion returns the embedded engine version.
func PinnedVersion() string {
	v, _ := loadPinnedGitVersion()
	return v
}

func loadPinnedGitVersion() (string, error) {
	pinOnce.Do(func() {
		data, err := config.Read(config.GitEnginePin)
		if err != nil {
			pinErr = err
			return
		}
		var p pinFile
		if err := config.DecodeYAML(data, &p); err != nil {
			pinErr = err
			return
		}
		pinGit = strings.TrimSpace(p.GitVersion)
		if pinGit == "" {
			pinErr = fmt.Errorf("gitengine: pin.yaml missing git_version")
			return
		}
		platform := p.Platforms[platformKey(runtime.GOOS, runtime.GOARCH)]
		pinReportedGit = strings.TrimSpace(platform.ReportedVersion)
		if pinReportedGit == "" {
			pinErr = fmt.Errorf("gitengine: pin.yaml missing reported version for %s/%s", runtime.GOOS, runtime.GOARCH)
		}
	})
	return pinGit, pinErr
}

func loadPinnedReportedVersion() (string, error) {
	if _, err := loadPinnedGitVersion(); err != nil {
		return "", err
	}
	return pinReportedGit, nil
}

func platformKey(goos, goarch string) string {
	return goos + "-" + goarch
}
