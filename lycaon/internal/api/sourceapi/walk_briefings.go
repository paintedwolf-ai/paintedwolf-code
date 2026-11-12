package sourceapi

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/stepbriefing"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// attachBriefings adds each step's explanation target and any briefing
// already stored for it. A Walk read never starts or links one.
func (s *History) attachBriefings(ctx context.Context, projectID string, out *wire.SourceWalkResponse) error {
	if s.Briefings == nil {
		return nil
	}
	out.BriefingTargets = stepbriefing.Targets(*out)
	epoch, err := s.Briefings.Store.PeekEpoch(ctx, projectID)
	if err != nil {
		return err
	}
	out.BriefingEpoch = strconv.FormatInt(epoch, 10)
	profile, err := s.Briefings.Registry.Profile(stepbriefing.ProfileID)
	// A host without step briefings serves Walk as it is.
	if errors.Is(err, briefing.ErrUnknownProfile) {
		return nil
	}
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(out.BriefingTargets))
	for _, target := range out.BriefingTargets {
		keys = append(keys, target.TargetKey)
	}
	slices.Sort(keys)
	stored, err := s.Briefings.Store.ListTargets(ctx, projectID, stepbriefing.ProfileID, keys)
	if err != nil {
		return err
	}
	for _, target := range stored {
		snapshot, err := s.Briefings.Store.Snapshot(ctx, briefingstore.SnapshotQuery{ProjectID: projectID, ProfileID: profile.ID(),
			Version: profile.Version(), Policy: profile.Policy(), TargetKey: target.TargetKey})
		if err != nil {
			return err
		}
		out.Briefings = append(out.Briefings, snapshot)
	}
	return nil
}
