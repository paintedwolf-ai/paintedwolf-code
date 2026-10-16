package main

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/extpacks"
	"gopkg.in/yaml.v3"
)

func checkReleasedSuite(release, current releaseSource, file string) error {
	before, err := release.show(file)
	if err != nil {
		return err
	}
	after, err := current.show(file)
	if err != nil {
		return err
	}
	var old, next extpacks.MetaPackManifest
	if err := yaml.Unmarshal(before, &old); err != nil {
		return err
	}
	if err := yaml.Unmarshal(after, &next); err != nil {
		return err
	}
	prior, err := semver.StrictNewVersion(old.Version)
	if err != nil {
		return err
	}
	version, err := semver.StrictNewVersion(next.Version)
	if err != nil {
		return err
	}
	if old.ID != next.ID || version.LessThan(prior) {
		return fmt.Errorf("suite %s identity or version regressed", old.ID)
	}
	if version.Equal(prior) && !bytes.Equal(before, after) {
		return fmt.Errorf("suite %s changed without a version bump", old.ID)
	}
	host, err := sourceExtensionAPI(current)
	if err != nil {
		return err
	}
	constraint, err := semver.NewConstraint(next.Compatibility.ExtensionAPI)
	if err != nil {
		return err
	}
	if !constraint.Check(host) {
		return fmt.Errorf("suite %s requires unavailable extension API", next.ID)
	}
	return nil
}

type anchorVariable struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
}

type anchorContract struct {
	ID       string   `yaml:"id"`
	Trigger  string   `yaml:"trigger_class"`
	Surface  string   `yaml:"surface"`
	Planes   []string `yaml:"planes"`
	Envelope struct {
		Vars map[string]anchorVariable `yaml:"vars"`
	} `yaml:"envelope"`
}

type anchorDocument struct {
	Version string           `yaml:"schema_version"`
	Anchors []anchorContract `yaml:"anchors"`
}

func checkAnchorVersions(release, current releaseSource) error {
	read := func(source releaseSource) (anchorDocument, *semver.Version, error) {
		var document anchorDocument
		raw, err := source.show(packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml")
		if err != nil {
			return document, nil, err
		}
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return document, nil, err
		}
		version, err := semver.StrictNewVersion(document.Version + ".0")
		return document, version, err
	}
	before, old, err := read(release)
	if err != nil {
		return err
	}
	after, next, err := read(current)
	if err != nil {
		return err
	}
	if next.LessThan(old) {
		return fmt.Errorf("anchor catalog version regressed")
	}
	anchors := map[string]anchorContract{}
	for _, anchor := range after.Anchors {
		slices.Sort(anchor.Planes)
		anchors[anchor.ID] = anchor
	}
	changed, breaking := len(before.Anchors) != len(after.Anchors), false
	for _, prior := range before.Anchors {
		slices.Sort(prior.Planes)
		now, exists := anchors[prior.ID]
		if !exists {
			breaking = true
			continue
		}
		changed = changed || !reflect.DeepEqual(prior, now)
		if prior.Trigger != now.Trigger || prior.Surface != now.Surface || !slices.Equal(prior.Planes, now.Planes) {
			breaking = true
		}
		for name, variable := range prior.Envelope.Vars {
			current, ok := now.Envelope.Vars[name]
			if !ok || current.Type != variable.Type || current.Required != variable.Required {
				breaking = true
			}
		}
		for name, variable := range now.Envelope.Vars {
			if _, ok := prior.Envelope.Vars[name]; !ok && variable.Required {
				breaking = true
			}
		}
	}
	if breaking && next.Major() <= old.Major() {
		return fmt.Errorf("breaking anchor contract requires a major catalog version")
	}
	if changed && !next.GreaterThan(old) {
		return fmt.Errorf("changed anchor contract requires a catalog version advance")
	}
	return nil
}
