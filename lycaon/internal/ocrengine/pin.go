package ocrengine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// Pin is the committed engine descriptor: the producer release it selects and
// the exact payload the host accepts on each platform.
type Pin struct {
	RecognitionLanguages []string               `yaml:"recognition_languages"`
	EngineVersion        string                 `yaml:"engine_version"`
	Producer             pinProducer            `yaml:"producer"`
	TrainedDataSource    pinDataSource          `yaml:"trained_data_source,omitempty"`
	TrainedData          map[string]pinPayload  `yaml:"trained_data"`
	Platforms            map[string]PlatformPin `yaml:"platforms"`
}

type pinDataSource struct {
	Repository string `yaml:"repository"`
	Commit     string `yaml:"commit"`
}

type pinProducer struct {
	Repository string `yaml:"repository"`
	Tag        string `yaml:"tag"`
	Commit     string `yaml:"commit"`
}

type pinPayload struct {
	Source string `yaml:"source,omitempty"`
	SHA256 string `yaml:"sha256"`
	Bytes  int64  `yaml:"bytes"`
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
		pinVal, pinErr = parsePin(data)
	})
	return pinVal, pinErr
}

func parsePin(data []byte) (*Pin, error) {
	var p Pin
	if err := config.DecodeYAML(data, &p); err != nil {
		return nil, fmt.Errorf("ocrengine: parse pin: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Pin) validate() error {
	if strings.TrimSpace(p.EngineVersion) == "" {
		return fmt.Errorf("ocrengine: pin.yaml missing engine_version")
	}
	if strings.TrimSpace(p.Producer.Repository) == "" || p.Producer.Tag != "v"+p.EngineVersion || !validCommit(p.Producer.Commit) {
		return fmt.Errorf("ocrengine: pin.yaml must name the producer repository, its commit, and the tag v%s", p.EngineVersion)
	}
	return p.validateLanguages()
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
	payloads := []Payload{{Rel: binaryRel, SHA256: plat.BinarySHA256, Bytes: plat.BinaryBytes}}
	for _, language := range append(append([]string{}, p.RecognitionLanguages...), "osd") {
		data := p.TrainedData[language+"_traineddata"]
		payloads = append(payloads, Payload{Rel: "tessdata/" + language + ".traineddata", SHA256: data.SHA256, Bytes: data.Bytes})
	}
	return payloads
}

func validDigest(s string) bool {
	return len(s) == 64 && lowerHex(s)
}

func validCommit(s string) bool {
	return len(s) == 40 && lowerHex(s)
}

func lowerHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
