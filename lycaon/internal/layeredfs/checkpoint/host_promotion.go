package checkpoint

import "context"

// HostPromotion joins immutable checkpoint effects to the host's SQL commit.
// It owns undo independently of the component's roll-forward checkpoint journal.
type HostPromotion struct {
	checkpoint        *Promoter
	store             *Store
	targets           *promotionTargets
	authorizedGitDirs []string
}

func NewHostPromotion(checkpoint *Promoter) *HostPromotion {
	return &HostPromotion{checkpoint: checkpoint, store: checkpoint.cfg.Store, targets: checkpoint.targets, authorizedGitDirs: checkpoint.cfg.AuthorizedGitDirs}
}

// Apply persists undo and staged post-images before any checkpoint publication.
// SQL completion remains the caller's commit boundary.
func (host *HostPromotion) Apply(ctx context.Context, req ApplyRequest) (*PromotionJournalRecord, error) {
	host.checkpoint.applyMu.Lock()
	defer host.checkpoint.applyMu.Unlock()
	if err := host.prepare(ctx, req.Preview); err != nil {
		return nil, err
	}
	host.targets.recordPostimage = host.recordHostPostimage
	defer func() { host.targets.recordPostimage = nil }()
	importRefs := func(ctx context.Context, _ string, workerID string, refs PrivateGitSnapshot) (map[string]string, error) {
		return ImportAndValidateCheckpointGitRefs(ctx, host.store.RootDir(), req.Preview.CheckpointID, workerID, refs, host.targets.roots, host.authorizedGitDirs...)
	}
	return host.checkpoint.apply(ctx, req, importRefs)
}
