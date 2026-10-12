package structure

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const declarationPath = "lycaon/test/contract/structure/declarations.yaml"

type character string

const (
	subsystem character = "subsystem"
	family    character = "family"
	container character = "container"
)

// declaration states what kind of thing one directory under lycaon/internal is.
// A subsystem may be imported from outside only through its entry points. A
// family holds alternatives behind one contract package. A container's members
// are governed individually.
type declaration struct {
	Kind        character `yaml:"kind"`
	Description string    `yaml:"description"`
	EntryPoints []string  `yaml:"entry_points"`
	Contract    string    `yaml:"contract"`
}

// declarations maps a directory under lycaon/internal, relative to it, to its
// declaration. Directories absent from the map are containers.
type declarations map[string]declaration

// loadDeclarations reads and validates the declaration document through read.
func loadDeclarations(read func(string) ([]byte, error)) (declarations, error) {
	raw, err := read(declarationPath)
	if err != nil {
		return nil, fmt.Errorf("read structure declarations: %w", err)
	}
	var parsed declarations
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode structure declarations: %w", err)
	}
	names := make([]string, 0, len(parsed))
	for name := range parsed {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := validate(name, parsed[name]); err != nil {
			return nil, fmt.Errorf("declaration %s: %w", name, err)
		}
	}
	return parsed, nil
}

func validate(name string, d declaration) error {
	switch {
	case name == "internal":
	case strings.HasPrefix(name, "internal/") && !strings.Contains(strings.TrimPrefix(name, "internal/"), "/") && cleanDir(name):
	default:
		return fmt.Errorf("declaration %q is not a directory directly under lycaon/internal", name)
	}
	switch d.Kind {
	case subsystem:
		if strings.TrimSpace(d.Description) == "" {
			return fmt.Errorf("subsystem requires a description")
		}
		if len(d.EntryPoints) == 0 {
			return fmt.Errorf("subsystem requires entry_points")
		}
		for _, entry := range d.EntryPoints {
			if !importPath(entry) {
				return fmt.Errorf("invalid entry point %q", entry)
			}
		}
	case family:
		if !importPath(d.Contract) {
			return fmt.Errorf("family requires its contract package")
		}
	case container:
		if len(d.EntryPoints) != 0 || d.Contract != "" {
			return fmt.Errorf("container declares no entry points or contract")
		}
	default:
		return fmt.Errorf("unknown kind %q", d.Kind)
	}
	return nil
}

func cleanDir(name string) bool {
	return name == path.Clean(name) && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, ".") &&
		!strings.ContainsAny(name, "\\\x00\r\n\t")
}

func importPath(value string) bool {
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return strings.HasPrefix(value, "github.com/lycaon/lycaon/") || value == "github.com/lycaon/lycaon"
}