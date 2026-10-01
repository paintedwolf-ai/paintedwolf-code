package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

type projectProvenance struct {
	Origin        string            `yaml:"origin"`
	Revision      string            `yaml:"revision"`
	License       string            `yaml:"license"`
	SamplingUnit  string            `yaml:"sampling_unit"`
	Adjudicator   string            `yaml:"adjudicator"`
	UsedForTuning bool              `yaml:"used_for_tuning"`
	SourceSHA256  map[string]string `yaml:"source_sha256"`
}

func validateHeldOutProject(project projectCase, units map[string]bool) error {
	p := project.Provenance
	if p == nil || p.UsedForTuning || strings.TrimSpace(p.License) == "" || strings.TrimSpace(p.Adjudicator) == "" || p.SamplingUnit == "" || units[p.SamplingUnit] || project.Framework == "" || len(project.Families) == 0 {
		return fmt.Errorf("%s: held-out evidence needs untuned provenance, a distinct sampling unit, license, adjudicator, framework, and families", project.Name)
	}
	if err := validateProjectProvenance(project); err != nil {
		return err
	}
	units[p.SamplingUnit] = true
	return nil
}

func validateProjectProvenance(project projectCase) error {
	p := project.Provenance
	origin, err := url.Parse(p.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil {
		return fmt.Errorf("%s: provenance origin must be an HTTPS source URL", project.Name)
	}
	if _, err := hex.DecodeString(p.Revision); err != nil || (len(p.Revision) != 40 && len(p.Revision) != 64) {
		return fmt.Errorf("%s: provenance requires an immutable source revision", project.Name)
	}
	if len(p.SourceSHA256) != len(project.Files) {
		return fmt.Errorf("%s: provenance must bind every source file", project.Name)
	}
	for path, source := range project.Files {
		if p.SourceSHA256[path] != fmt.Sprintf("%x", sha256.Sum256([]byte(source))) {
			return fmt.Errorf("%s: source differs from its reviewed provenance: %s", project.Name, path)
		}
	}
	return nil
}
