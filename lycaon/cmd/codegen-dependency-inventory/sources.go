package main

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// sourceRef locates one pinned or upstream value. Policy files write it as a
// one-line spec such as "shell scripts/lint-go.sh GOLANGCI_VERSION", or as
// {label, from} when the cell needs a label.
type sourceRef struct {
	Kind    string
	Label   string
	File    string
	Name    string
	Key     string
	Count   bool
	Repo    string
	Strip   string
	Pattern string
	Track   string
	Channel string
}

const (
	kindGoModule = "go-module"
	kindNPM      = "npm"
	kindCrate    = "crate"
)

// sourceGrammar lists each kind's positional arguments; a trailing "?" marks
// an optional one.
var sourceGrammar = map[string][]string{
	"file":               {"file"},
	"shell":              {"file", "name"},
	"go-const":           {"file", "name"},
	"toml":               {"file", "key"},
	"yaml":               {"file", "key"},
	"yaml-count":         {"file", "key"},
	"json":               {"file", "key"},
	"go-directive":       {"file"},
	"go-require":         {"file", "name"},
	"npm-lock":           {"file", "name"},
	"cargo-lock":         {"file", "name"},
	kindGoModule:         {"name"},
	kindNPM:              {"name"},
	kindCrate:            {"name"},
	"github-release":     {"repo", "strip?"},
	"github-tag":         {"repo", "pattern", "strip?"},
	"github-commit":      {"repo"},
	"go-release":         {"track"},
	"node-release":       {},
	"rust-stable":        {},
	"chrome-for-testing": {"channel"},
}

var (
	pinKinds = map[string]bool{
		"file": true, "shell": true, "go-const": true, "toml": true, "yaml": true, "yaml-count": true, "json": true,
		"go-directive": true, "go-require": true, "npm-lock": true, "cargo-lock": true,
	}
	upstreamKinds = map[string]bool{
		kindGoModule: true, kindNPM: true, kindCrate: true, "github-release": true, "github-tag": true,
		"github-commit": true, "go-release": true, "node-release": true, "rust-stable": true, "chrome-for-testing": true,
	}
)

// UnmarshalYAML accepts "kind args..." or {label: ..., from: "kind args..."}.
func (s *sourceRef) UnmarshalYAML(node *yaml.Node) error {
	var spec, label string
	switch node.Kind {
	case yaml.ScalarNode:
		spec = node.Value
	case yaml.MappingNode:
		var labeled struct {
			Label string `yaml:"label"`
			From  string `yaml:"from"`
		}
		if err := node.Decode(&labeled); err != nil {
			return fmt.Errorf("line %d: %w", node.Line, err)
		}
		spec, label = labeled.From, labeled.Label
	default:
		return fmt.Errorf("line %d: a source is a string or {label, from}", node.Line)
	}
	parsed, err := parseSource(spec)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	parsed.Label = label
	*s = parsed
	return nil
}

func parseSource(spec string) (sourceRef, error) {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return sourceRef{}, errors.New("empty source")
	}
	kind, args := fields[0], fields[1:]
	grammar, ok := sourceGrammar[kind]
	if !ok {
		return sourceRef{}, fmt.Errorf("unknown source kind %q in %q", kind, spec)
	}
	required := 0
	for _, a := range grammar {
		if !strings.HasSuffix(a, "?") {
			required++
		}
	}
	if len(args) < required || len(args) > len(grammar) {
		return sourceRef{}, fmt.Errorf("%q: want %s %s", spec, kind, strings.Join(grammar, " "))
	}
	ref := sourceRef{Kind: kind}
	if kind == "yaml-count" {
		ref.Kind, ref.Count = "yaml", true
	}
	for i, arg := range args {
		switch strings.TrimSuffix(grammar[i], "?") {
		case "file":
			ref.File = arg
		case "name":
			ref.Name = arg
		case "key":
			ref.Key = arg
		case "repo":
			ref.Repo = arg
		case "strip":
			ref.Strip = arg
		case "pattern":
			ref.Pattern = arg
		case "track":
			ref.Track = arg
		case "channel":
			ref.Channel = arg
		}
	}
	return ref, nil
}

func sourcesIn(where, list string, refs []sourceRef, kinds map[string]bool) error {
	var errs []error
	for _, ref := range refs {
		kind := ref.Kind
		if ref.Count {
			kind = "yaml-count"
		}
		if !kinds[kind] {
			errs = append(errs, fmt.Errorf("%s: %s cannot use source kind %q", where, list, kind))
		}
	}
	return errors.Join(errs...)
}
