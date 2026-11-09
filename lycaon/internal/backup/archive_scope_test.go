package backup

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/localdata"
)

func TestArchiveVersionsRequireTheirExactReplacementScope(t *testing.T) {
	legacy := Manifest{FormatVersion: 1, ReplaceRelPaths: slices.Clone(archiveV1Files),
		ReplaceRelDirs: slices.Clone(archiveV1Directories),
		Files:          []FileEntry{{RelPath: "store.db", Kind: fileKindRegular, Mode: 0600, SHA256: strings.Repeat("a", 64)}},
	}
	for _, name := range []string{"released", "current", "missing file", "duplicate file", "future file", "missing directory", "unknown version"} {
		t.Run(name, func(t *testing.T) {
			manifest := legacy
			manifest.ReplaceRelPaths = slices.Clone(legacy.ReplaceRelPaths)
			manifest.ReplaceRelDirs = slices.Clone(legacy.ReplaceRelDirs)
			switch name {
			case "current":
				manifest = upgradeArchiveScope(manifest)
			case "missing file":
				manifest.ReplaceRelPaths = manifest.ReplaceRelPaths[1:]
			case "duplicate file":
				manifest.ReplaceRelPaths = append(manifest.ReplaceRelPaths, "store.db")
			case "future file":
				manifest.ReplaceRelPaths = append(manifest.ReplaceRelPaths, "command-programs.yaml")
			case "missing directory":
				manifest.ReplaceRelDirs = manifest.ReplaceRelDirs[1:]
			case "unknown version":
				manifest.FormatVersion = 3
			}
			err := validateManifestEntries(manifest, localdata.BackupRelPaths(), localdata.BackupRelDirs(), map[string]uint64{"store.db": 0})
			accepted := name == "released" || name == "current"
			if (err == nil) != accepted {
				t.Fatalf("scope verdict = %v, want accepted %v", err, accepted)
			}
		})
	}
	upgraded := upgradeArchiveScope(legacy)
	if upgraded.FormatVersion != 2 || !slices.Contains(upgraded.ReplaceRelPaths, "command-programs.yaml") || slices.Contains(legacy.ReplaceRelPaths, "command-programs.yaml") {
		t.Fatal("scope upgrade must add the new path without mutating the released manifest")
	}
	if !slices.Contains(upgraded.ReplaceRelDirs, "worker-recovery") || slices.Contains(legacy.ReplaceRelDirs, "worker-recovery") {
		t.Fatal("scope upgrade must add worker-recovery without mutating the released manifest")
	}
}

type GitClosureStats = gitClosureStats

var WithGitClosureStats = withGitClosureStats

