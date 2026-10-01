package skills

import (
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

func skillDiscovery(tctx tools.ToolContext, need, cursor, status string, failure turnload.RankingFailure, catalog []skills.Skill) (string, error) {
	entries := make([]turnload.DiscoveryEntry, 0, len(catalog))
	for _, sk := range catalog {
		entries = append(entries, turnload.DiscoveryEntry{Name: sk.Name, Description: sk.Description})
	}
	page, err := tools.DiscoveryPage(tctx, "skills_read", need, cursor, status, failure, entries)
	if err != nil {
		return "", err
	}
	raw, err := surveyjson.Marshal(struct {
		Discovery *turnload.Discovery `json:"discovery"`
	}{page})
	return string(raw), err
}
