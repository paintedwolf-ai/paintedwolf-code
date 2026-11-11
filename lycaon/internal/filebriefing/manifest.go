package filebriefing

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/lycaon/lycaon/internal/briefing"
)

// ProfileID names file summaries among briefing profiles.
const ProfileID = "file_summary"

const (
	profileVersion = "1"
	sectionID      = "summary"
)

// materialManifest describes the file summary question: the file's facts at
// its source revision, how material was selected, and the exact prompt.
func materialManifest(input Input, facts fileFacts, coverage materialCoverage, prompt string, cfg Config) briefing.Manifest {
	inclusion := briefing.IncludedWhole
	if !coverage.SourceComplete {
		inclusion = briefing.IncludedTruncated
	}
	return briefing.Manifest{
		ProfileID: ProfileID, ProfileVersion: profileVersion, SectionID: sectionID,
		Template: briefing.Template{ID: ProfileID, Version: textDigest(cfg.SystemPrompt)},
		Contract: briefing.Contract{MaxRunes: cfg.Generation.MaxOutputChars},
		Items: []briefing.Item{
			{Key: "file", Role: briefing.RoleStructure, SourceKind: "file", Ref: input.Path, Revision: input.SourceSHA256, Inclusion: briefing.IncludedWhole, Text: jsonString(facts)},
			{Key: "selection", Role: briefing.RoleContext, SourceKind: "selection", Ref: input.Path, Inclusion: briefing.IncludedWhole, Text: jsonString(coverage)},
			{Key: "material", Role: briefing.RoleObservation, SourceKind: "file_excerpt", Ref: input.Path, Revision: input.SourceSHA256, Inclusion: inclusion, Text: prompt},
		},
	}
}

func textDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}
