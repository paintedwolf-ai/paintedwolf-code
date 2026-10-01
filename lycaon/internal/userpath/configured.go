package userpath

import (
	"fmt"
	"path/filepath"
)

// Configured validates a caller-supplied PATH without starting a shell.
func Configured(raw string, maxEntries int) (Snapshot, error) {
	parts := filepath.SplitList(raw)
	if len(parts) == 0 || maxEntries <= 0 || len(parts) > maxEntries {
		return Snapshot{}, fmt.Errorf("command PATH requires between one and %d entries", maxEntries)
	}
	for _, entry := range parts {
		if !isAcceptableEntry(entry) {
			return Snapshot{}, fmt.Errorf("command PATH requires absolute entries without control characters")
		}
	}
	return Snapshot{entries: parseEntries(raw, maxEntries), source: SourceConfigured}, nil
}
