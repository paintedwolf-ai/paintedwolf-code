package workerruntime

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/tools"
)

// A worker leg names exactly what its request offers: skills_read only when
// the profile's compiled skill catalog holds a skill.
func TestOfferedLegToolsFollowTheSkillCatalog(t *testing.T) {
	metas := []tools.ToolMeta{{Name: "command"}, {Name: "skills_read"}, {Name: " "}, {Name: "complete_leg"}}
	for _, tc := range []struct {
		name    string
		machine inject.Machine
		want    []string
	}{
		{"empty catalog", inject.Machine{ProfileID: "explore_readonly", SkillCount: 0}, []string{"command", "complete_leg"}},
		{"catalog with a skill", inject.Machine{ProfileID: "explore_readonly", SkillCount: 1}, []string{"command", "skills_read", "complete_leg"}},
		{"machine not compiled", inject.Machine{}, []string{"command", "skills_read", "complete_leg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := offeredLegTools(metas, tc.machine); !slices.Equal(got, tc.want) {
				t.Fatalf("offered leg tools = %v, want %v", got, tc.want)
			}
		})
	}
}
