package ocrengine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// Pin is the committed engine descriptor: the sources the archive was built
// from and the exact payload the host accepts on each platform.
type Pin struct {
	EngineVersion   string                 `yaml:"engine_version"`
	BuildImage      string                 `yaml:"build_image"`
	SourceDateEpoch int64                  `yaml:"source_date_epoch"`
	Sources         map[string]pinSource   `yaml:"sources"`
	Platforms       map[string]PlatformPin `yaml:"platforms"`
}

type pinSource struct {
	Version      string `yaml:"version"`
	URL          string `yaml:"url"`
	SHA256       string `yaml:"sha256"`
	Bytes        int64  `yaml:"bytes"`
	Commit       string `yaml:"commit"`
	License      string `yaml:"license"`
	CopyrightSHA string `yaml:"copyright_sha256"`
	GPL3SHA      string `yaml:"gpl3_sha256"`
	ExceptionSHA string `yaml:"exception_sha256"`
}

// PlatformPin is one platform's archive and the executable inside it.
type PlatformPin struct {
	URL          string `yaml:"url"`
	SHA256       string `yaml:"sha256"`
	Bytes        int64  `yaml:"bytes"`
	BinarySHA256 string `yaml:"binary_sha256"`
	BinaryBytes  int64  `yaml:"binary_bytes"`
}

// Payload is one file the host verifies before running the engine.
type Payload struct {
	Rel    string
	SHA256 string
	Bytes  int64
}

// Trained data files every platform ships, relative to the engine root.
const (
	englishTrainedDataRel = "tessdata/eng.traineddata"
	osdTrainedDataRel     = "tessdata/osd.traineddata"
	binaryRel             = "bin/tesseract"
)

var (
	pinOnce sync.Once
	pinVal  *Pin
	pinErr  error
)

// LoadPin returns the embedded engine pin.
func LoadPin() (*Pin, error) {
	pinOnce.Do(func() {
		data, err := config.Read(config.OCREnginePin)
		if err != nil {
			pinErr = err
			return
		}
		var p Pin
		if err := config.DecodeYAML(data, &p); err != nil {
			pinErr = fmt.Errorf("ocrengine: parse pin: %w", err)
			return
		}
		if err := p.validate(); err != nil {
			pinErr = err
			return
		}
		pinVal = &p
	})
	return pinVal, pinErr
}

func (p *Pin) validate() error {
	if strings.TrimSpace(p.EngineVersion) == "" {
		return fmt.Errorf("ocrengine: pin.yaml missing engine_version")
	}
	for _, name := range []string{"eng_traineddata", "osd_traineddata"} {
		src, ok := p.Sources[name]
		if !ok || !validDigest(src.SHA256) || src.Bytes <= 0 {
			return fmt.Errorf("ocrengine: pin.yaml needs a digest and size for %s", name)
		}
	}
	return nil
}

// Platform returns the pin for one GOOS/GOARCH, or false when the engine is
// not built for it. A platform whose archive digest is still empty counts as
// unpublished and is not usable.
func (p *Pin) Platform(goos, goarch string) (PlatformPin, bool) {
	plat, ok := p.Platforms[goos+"-"+goarch]
	if !ok || !validDigest(plat.BinarySHA256) || plat.BinaryBytes <= 0 {
		return PlatformPin{}, false
	}
	return plat, true
}

// Payloads lists the files the host verifies for one platform.
func (p *Pin) Payloads(plat PlatformPin) []Payload {
	eng, osd := p.Sources["eng_traineddata"], p.Sources["osd_traineddata"]
	return []Payload{
		{Rel: binaryRel, SHA256: plat.BinarySHA256, Bytes: plat.BinaryBytes},
		{Rel: englishTrainedDataRel, SHA256: eng.SHA256, Bytes: eng.Bytes},
		{Rel: osdTrainedDataRel, SHA256: osd.SHA256, Bytes: osd.Bytes},
	}
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
