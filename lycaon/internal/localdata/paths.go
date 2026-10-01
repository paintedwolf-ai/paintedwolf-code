package localdata

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/enginepaths"
)

const webIndexFileName = "web-index.db"

var (
	// durableRelPaths survive local data clears.
	durableRelPaths = entryNames(func(e ConfigRootEntry) bool {
		return !e.IsDir && (e.Class == ClassDurableFile || e.Class == ClassArchiveExcluded)
	})
	durableRelDirs     = entryNames(func(e ConfigRootEntry) bool { return e.IsDir && e.Class == ClassDurableDir })
	backupRelPaths     = entryNames(func(e ConfigRootEntry) bool { return !e.IsDir && e.IncludeInBackup })
	backupRelDirs      = entryNames(func(e ConfigRootEntry) bool { return e.IsDir && e.IncludeInBackup })
	credentialRelPaths = entryNames(func(e ConfigRootEntry) bool { return !e.IsDir && e.IsSecret })
)

func webIndexPath(base string) string {
	return filepath.Join(base, webIndexFileName)
}

func sourceObservationsPath(base string) string {
	return filepath.Join(base, enginepaths.SourceObservationsDBName)
}

func sourceObservationPaths(base string) []string {
	path := sourceObservationsPath(base)
	out := []string{path}
	for _, suffix := range sqliteJournalSuffixes {
		out = append(out, path+suffix)
	}
	return append(out, enginepaths.RepoOrientationRootUnder(base))
}

// DurableAbsPaths resolves protected files under base.
func DurableAbsPaths(base string) []string {
	out := make([]string, 0, len(durableRelPaths))
	for _, rel := range durableRelPaths {
		out = append(out, filepath.Join(base, rel))
	}
	return out
}

// BackupRelDirs returns directories archived per file.
func BackupRelDirs() []string {
	return append([]string(nil), backupRelDirs...)
}

// BackupRelPaths returns files included in backups.
func BackupRelPaths() []string {
	return append([]string(nil), backupRelPaths...)
}

// IsCredentialRel reports files that hold secret values.
func IsCredentialRel(rel string) bool {
	return slices.Contains(credentialRelPaths, filepath.Base(rel))
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func dirNonEmpty(p string) bool {
	entries, err := os.ReadDir(p)
	return err == nil && len(entries) > 0
}

// CredentialRelPaths returns the secret-bearing durable relative paths.
func CredentialRelPaths() []string {
	return append([]string(nil), credentialRelPaths...)
}
