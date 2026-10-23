package packagegeneration

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

func (seal *RuntimeSeal) Validate() error {
	if seal == nil || seal.FormatVersion != RuntimeSealVersion {
		return errors.New("runtime seal: unsupported format")
	}
	if seal.Architecture != "x86_64" && seal.Architecture != "aarch64" {
		return errors.New("runtime seal: unsupported architecture")
	}
	if !runtimeHash(seal.BaseManifestSHA256) || !runtimeHash(seal.InventorySHA256) ||
		!runtimeText(seal.ProducerVersion, 128) || strings.TrimSpace(seal.ProducerVersion) != seal.ProducerVersion {
		return errors.New("runtime seal: invalid identity")
	}
	count := len(seal.Sources) + len(seal.Files) + len(seal.Aliases) + len(seal.Anchors) + len(seal.Links)
	if count > RuntimeSealMaxEntries || len(seal.Sources) == 0 || len(seal.Files) == 0 ||
		len(seal.Aliases) == 0 || len(seal.Anchors) == 0 {
		return errors.New("runtime seal: invalid entry count")
	}
	files, err := seal.validateFiles()
	if err != nil {
		return err
	}
	inventory, ok := files[seal.InventoryPath]
	if !ok || inventory.Kind != "file" || *inventory.SHA256 != seal.InventorySHA256 || *inventory.Size == 0 {
		return errors.New("runtime seal: inventory differs from final file")
	}
	seen := make(map[string]bool, len(seal.Sources))
	for _, source := range seal.Sources {
		if !runtimeAbsolute(source.Path) || seen[source.Path] || !runtimeHash(source.SHA256) || source.Size < 0 {
			return fmt.Errorf("runtime seal: invalid or duplicate source %q", source.Path)
		}
		seen[source.Path] = true
		if err := source.Origin.validate(source.Path); err != nil {
			return err
		}
		file, ok := files[source.CapturedPath]
		if !ok || file.Kind != "file" || *file.SHA256 != source.SHA256 || *file.Size != source.Size {
			return fmt.Errorf("runtime seal: captured source differs from final file %q", source.Path)
		}
	}
	if err := seal.validateAliases(files); err != nil {
		return err
	}
	if err := seal.validateLinks(files); err != nil {
		return err
	}
	roles := make(map[string]bool, len(seal.Anchors))
	for _, anchor := range seal.Anchors {
		file, ok := files[anchor.Path]
		if !runtimeRole(anchor.Role) || roles[anchor.Role] || !runtimeHash(anchor.SHA256) || anchor.Size <= 0 ||
			!ok || file.Kind != "file" || *file.SHA256 != anchor.SHA256 || *file.Size != anchor.Size {
			return fmt.Errorf("runtime seal: invalid or duplicate anchor %q", anchor.Role)
		}
		roles[anchor.Role] = true
	}
	return nil
}

func (origin RuntimeOrigin) validate(source string) error {
	if !runtimeAbsolute(origin.SelectedRoot) ||
		(source != origin.SelectedRoot && !strings.HasPrefix(source, origin.SelectedRoot+"/")) {
		return errors.New("runtime seal: source outside selected input")
	}
	switch origin.Kind {
	case "native":
		if origin.StoreRoot != "" || origin.OutputPath != "" {
			return errors.New("runtime seal: native source has Nix fields")
		}
	case "nix":
		if !runtimeAbsolute(origin.StoreRoot) || origin.OutputPath != origin.SelectedRoot ||
			path.Dir(origin.OutputPath) != origin.StoreRoot {
			return errors.New("runtime seal: invalid selected Nix output")
		}
	default:
		return errors.New("runtime seal: unsupported source provenance")
	}
	return nil
}

func runtimeText(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}

func runtimeAbsolute(value string) bool {
	return runtimeText(value, 4095) && strings.HasPrefix(value, "/") &&
		value != "/" && path.Clean(value) == value
}

func runtimeRelative(value string) bool {
	return runtimeText(value, 4095) && !strings.HasPrefix(value, "/") &&
		value != "." && value != ".." && !strings.HasPrefix(value, "../") && path.Clean(value) == value
}

func runtimeHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func runtimeRole(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, part := range strings.Split(value, "-") {
		if part == "" {
			return false
		}
		for _, character := range part {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}
