package anchor_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// declaredRequestReader is the coordinator profile's request with every
// profile tool offered and no root, the request the kick goldens record.
func declaredRequestReader(t *testing.T) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	var offered []string
	for _, p := range profiles {
		if p.ID == prompts.CoordinatorProfileID {
			for name, ok := range p.Tools {
				if ok && !strings.HasSuffix(name, "*") {
					offered = append(offered, name)
				}
			}
		}
	}
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, WebSearch: true},
		prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, Offered: offered, Declared: true})
	testutil.FailErr(t, "declared reader", err)
	return reader
}
