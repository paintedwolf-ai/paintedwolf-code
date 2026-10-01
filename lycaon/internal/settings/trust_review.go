package settings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// SeenRecordsFor binds the scanned bytes to durable project root identities.
func SeenRecordsFor(p project.Project, scan projectcontrib.Manifest) map[string]project.SeenRecord {
	roots := map[string]project.Root{}
	for _, root := range p.Roots {
		roots[root.Path] = root
	}
	out := make(map[string]project.SeenRecord)
	for _, surface := range scan.Surfaces {
		files := make([]project.TrustReadFile, 0, len(surface.Files))
		seen := map[string]bool{}
		for _, file := range surface.Files {
			root := roots[file.RootPath]
			key := root.ID + "/" + file.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			files = append(files, project.TrustReadFile{RootID: root.ID, RootLabel: root.Label, Path: file.Path, Content: file.Content, SHA256: file.SHA256})
		}
		sort.Slice(files, func(i, j int) bool { return trustFileKey(files[i]) < trustFileKey(files[j]) })
		out[surface.ID] = project.SeenRecord{Stamp: trustFilesStamp(files), Files: files}
	}
	return out
}

func trustFileKey(file project.TrustReadFile) string { return file.RootID + "/" + file.Path }

func trustFilesStamp(files []project.TrustReadFile) string {
	if len(files) == 0 {
		return ""
	}
	type identity struct{ RootID, Path, SHA256 string }
	parts := make([]identity, 0, len(files))
	for _, file := range files {
		parts = append(parts, identity{file.RootID, file.Path, file.SHA256})
	}
	encoded, _ := json.Marshal(parts) //nolint:errchkjson // string-only identities always encode
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type trustReviewFile struct {
	file     project.TrustReadFile
	surfaces []wire.TrustSurfaceId
}

func trustReviewFiles(records map[string]project.SeenRecord) map[string]trustReviewFile {
	out := make(map[string]trustReviewFile)
	for _, spec := range projectcontrib.Registry() {
		for _, file := range records[spec.ID].Files {
			key := trustFileKey(file)
			row := out[key]
			row.file = file
			row.surfaces = append(row.surfaces, wire.TrustSurfaceId(spec.ID))
			out[key] = row
		}
	}
	return out
}

// TrustChanges compares the retained baseline, including removed files and roots.
func TrustChanges(before, after map[string]project.SeenRecord) []wire.TrustFileChange {
	previous, current := trustReviewFiles(before), trustReviewFiles(after)
	keys := make(map[string]bool, len(previous)+len(current))
	for key := range previous {
		keys[key] = true
	}
	for key := range current {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	changes := make([]wire.TrustFileChange, 0)
	for _, key := range ordered {
		old, had := previous[key]
		next, has := current[key]
		if had && has && old.file.SHA256 == next.file.SHA256 {
			continue
		}
		row := next
		kind := "modified"
		if !had {
			kind = "added"
		}
		if !has {
			kind, row = "removed", old
		}
		surfaces := append([]wire.TrustSurfaceId(nil), row.surfaces...)
		for _, id := range old.surfaces {
			found := false
			for _, existing := range surfaces {
				if existing == id {
					found = true
				}
			}
			if !found {
				surfaces = append(surfaces, id)
			}
		}
		changes = append(changes, wire.TrustFileChange{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String(), RootID: row.file.RootID, RootLabel: row.file.RootLabel, Path: row.file.Path, SurfaceIds: surfaces, Kind: kind, Before: old.file.Content, After: next.file.Content})
	}
	return changes
}

func StampTrustReadTime(records map[string]project.SeenRecord, at time.Time) {
	for id, record := range records {
		record.ReadAt = at.UTC().Format(time.RFC3339Nano)
		records[id] = record
	}
}

// TrustComparison retains the displayed comparison after its files are marked read.
func TrustComparison(p project.Project, current map[string]project.SeenRecord) (wire.ProjectTrustReview, int) {
	changes := TrustChanges(p.TrustSeen, current)
	unread := len(changes)
	if unread == 0 {
		previous := make(map[string]project.SeenRecord, len(p.TrustSeen))
		for id, record := range p.TrustSeen {
			record.Files = record.BeforeFiles
			previous[id] = record
		}
		changes = TrustChanges(previous, current)
	}
	encoded, _ := json.Marshal(changes) //nolint:errchkjson // plain change records always encode
	id := uuid.NewSHA1(uuid.NameSpaceOID, append([]byte(p.ID+"/"), encoded...)).String()
	return wire.ProjectTrustReview{ID: id, ProjectID: p.ID, Changes: changes}, unread
}

// RetainTrustComparison preserves the displayed baseline across acknowledgement.
func RetainTrustComparison(records, previous map[string]project.SeenRecord) {
	for id, record := range records {
		record.BeforeFiles = previous[id].Files
		records[id] = record
	}
}
