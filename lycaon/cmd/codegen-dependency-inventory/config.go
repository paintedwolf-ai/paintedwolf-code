package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// policy is the hand-authored dependency policy: policy.yaml, the prose
// Markdown beside it, and one file per section under sections/.
type policy struct {
	Title      string           `yaml:"title"`
	Quadrants  quadrantStances  `yaml:"quadrants"`
	Dependabot dependabotConfig `yaml:"dependabot"`
	Sections   []string         `yaml:"sections"`

	intro, ratings, runbooks string
	sections                 []sectionConfig
}

type quadrantStances struct {
	HighUrgencyHighFriction string `yaml:"high_urgency_high_friction"`
	HighUrgencyLowFriction  string `yaml:"high_urgency_low_friction"`
	LowUrgencyHighFriction  string `yaml:"low_urgency_high_friction"`
	LowUrgencyLowFriction   string `yaml:"low_urgency_low_friction"`
}

type dependabotConfig struct {
	Interval     string            `yaml:"interval"`
	CooldownDays int               `yaml:"cooldown_days"`
	Extra        []dependabotEntry `yaml:"extra"`
}

// dependabotEntry is one ecosystem block that no manifest section produces.
type dependabotEntry struct {
	Ecosystem string `yaml:"ecosystem"`
	Directory string `yaml:"directory"`
	Group     string `yaml:"group"`
}

type sectionConfig struct {
	Title    string               `yaml:"title"`
	Intro    string               `yaml:"intro"`
	Manifest *manifestConfig      `yaml:"manifest"`
	Groups   []groupConfig        `yaml:"groups"`
	Packages map[string]judgement `yaml:"packages"`
	Rows     []rowConfig          `yaml:"rows"`

	file string
}

type manifestConfig struct {
	Kind            string `yaml:"kind"`
	Path            string `yaml:"path"`
	Lock            string `yaml:"lock"`
	DependabotGroup string `yaml:"dependabot_group"`
}

type groupConfig struct {
	Title    string               `yaml:"title"`
	Intro    string               `yaml:"intro"`
	Packages map[string]judgement `yaml:"packages"`
	Rows     []rowConfig          `yaml:"rows"`
}

// judgement is everything a person decides about an entry; every field may be blank.
type judgement struct {
	Urgency        string `yaml:"urgency"`
	UrgencyReason  string `yaml:"urgency_reason"`
	Friction       string `yaml:"friction"`
	FrictionReason string `yaml:"friction_reason"`
	Updates        string `yaml:"updates"`
	Notes          string `yaml:"notes"`
}

type rowConfig struct {
	ID        string      `yaml:"id"`
	Name      string      `yaml:"name"`
	Pins      []sourceRef `yaml:"pins"`
	Upstream  []sourceRef `yaml:"upstream"`
	judgement `yaml:",inline"`
}

const (
	manifestGoMod = "gomod"
	manifestBun   = "bun"
	manifestCargo = "cargo"

	updatesHold      = "hold"
	updatesNoMajor   = "no-major"
	updatesPatchOnly = "patch-only"
)

var (
	urgencies    = map[string]bool{"critical": true, "high": true, "moderate": true, "low": true}
	frictions    = map[string]bool{"high": true, "moderate": true, "low": true}
	updateRules  = map[string]bool{updatesHold: true, updatesNoMajor: true, updatesPatchOnly: true}
	manifests    = map[string]bool{manifestGoMod: true, manifestBun: true, manifestCargo: true}
	rowIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func loadPolicy(dir string) (*policy, error) {
	var p policy
	if err := decodeStrict(filepath.Join(dir, "policy.yaml"), &p); err != nil {
		return nil, err
	}
	for name, dst := range map[string]*string{"intro.md": &p.intro, "ratings.md": &p.ratings, "runbooks.md": &p.runbooks} {
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- fixed policy file
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		*dst = string(data)
	}
	var errs []error
	for _, name := range p.Sections {
		s := sectionConfig{file: "sections/" + name}
		if err := decodeStrict(filepath.Join(dir, s.file), &s); err != nil {
			errs = append(errs, err)
			continue
		}
		p.sections = append(p.sections, s)
	}
	errs = append(errs, p.validate())
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &p, nil
}

func decodeStrict(path string, out any) error {
	data, err := os.ReadFile(path) // #nosec G304 -- file named by the dependency policy
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (p *policy) validate() error {
	var errs []error
	rowIDs := map[string]bool{}
	for _, s := range p.sections {
		if s.Manifest != nil && !manifests[s.Manifest.Kind] {
			errs = append(errs, fmt.Errorf("%s: manifest kind %q is not gomod, bun, or cargo", s.file, s.Manifest.Kind))
		}
		packages := []map[string]judgement{s.Packages}
		rows := s.Rows
		for _, g := range s.Groups {
			packages = append(packages, g.Packages)
			rows = append(rows, g.Rows...)
		}
		seen := map[string]bool{}
		for _, set := range packages {
			if len(set) > 0 && s.Manifest == nil {
				errs = append(errs, fmt.Errorf("%s: packages need a manifest", s.file))
			}
			for name, j := range set {
				if seen[name] {
					errs = append(errs, fmt.Errorf("%s: package %s appears twice", s.file, name))
				}
				seen[name] = true
				errs = append(errs, j.validate(s.file+": package "+name, s.Manifest != nil))
			}
		}
		for _, r := range rows {
			where := s.file + ": row " + r.ID
			if !rowIDPattern.MatchString(r.ID) || rowIDs[r.ID] {
				errs = append(errs, fmt.Errorf("%s: row ids must be unique kebab-case", where))
			}
			rowIDs[r.ID] = true
			errs = append(errs, r.validate(where, false), sourcesIn(where, "pins", r.Pins, pinKinds),
				sourcesIn(where, "upstream", r.Upstream, upstreamKinds))
		}
	}
	return errors.Join(errs...)
}

func (j judgement) validate(where string, dependabot bool) error {
	var errs []error
	if j.Urgency != "" && !urgencies[j.Urgency] {
		errs = append(errs, fmt.Errorf("%s: urgency %q is not critical, high, moderate, or low", where, j.Urgency))
	}
	if j.Friction != "" && !frictions[j.Friction] {
		errs = append(errs, fmt.Errorf("%s: friction %q is not high, moderate, or low", where, j.Friction))
	}
	if j.Updates != "" && (!dependabot || !updateRules[j.Updates]) {
		errs = append(errs, fmt.Errorf("%s: updates %q needs a manifest package and hold, no-major, or patch-only",
			where, j.Updates))
	}
	return errors.Join(errs...)
}

// notes renders the reasons and free notes as one table cell's prose.
func (j judgement) notes() string {
	var parts []string
	if r := strings.TrimSpace(j.UrgencyReason); r != "" {
		parts = append(parts, "**Urgency:** "+r)
	}
	if r := strings.TrimSpace(j.FrictionReason); r != "" {
		parts = append(parts, "**Friction:** "+r)
	}
	if n := strings.TrimSpace(j.Notes); n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, "\n\n")
}
