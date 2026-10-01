package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AliasSpellings returns host-supported aliases that name the same file.
func AliasSpellings(t *testing.T, target string) map[string]string {
	t.Helper()
	want, err := os.Stat(target)
	if err != nil {
		return nil
	}
	clean := filepath.Clean(target)
	candidates := map[string]string{
		"trailing separator": clean + string(filepath.Separator),
		"dot segment":        filepath.Join(clean, "."),
		"parent re-entry":    filepath.Join(clean, "..", filepath.Base(clean)),
		"double separator":   filepath.Dir(clean) + string(filepath.Separator) + string(filepath.Separator) + filepath.Base(clean),
		"firmlink":           filepath.Join("/System/Volumes/Data", clean),
		"upper case":         strings.ToUpper(clean),
		"lower case":         strings.ToLower(clean),
	}

	out := map[string]string{}
	for mechanism, alias := range candidates {
		if alias == clean || alias == target {
			continue
		}
		got, statErr := os.Stat(alias)
		if statErr != nil || !os.SameFile(want, got) {
			continue // This host does not offer that spelling.
		}
		out[mechanism] = alias
	}
	return out
}
