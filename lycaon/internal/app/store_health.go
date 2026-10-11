package app

import (
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hostlock"
)

// Store-coupled deletion requires both a valid claim and healthy facts.
type storeAccessGuard struct {
	store *db.Store
	claim *hostlock.Claim
}

func (g storeAccessGuard) Verify() error {
	if err := g.store.Failure(); err != nil {
		return err
	}
	if g.claim != nil {
		return g.claim.Verify()
	}
	return nil
}

func (a *ServeApp) storeFailed() <-chan struct{} {
	if a.resources == nil {
		return nil
	}
	return a.resources.db.Failed()
}

func (a *ServeApp) storeFailure() error {
	if a.resources == nil {
		return nil
	}
	return a.resources.db.Failure()
}
