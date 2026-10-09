package app

import (
	"github.com/lycaon/lycaon/internal/app/boards"
)

func (b *serveBuilder) wireBoardAndResearch() error {
	b.boards = boards.New(boards.Dependencies{
		Storage:                  b.storage,
		Sessions:                 b.sessions,
		Delegations:              b.delegations,
		Workflows:                b.workflows,
		Scanning:                 b.scanning,
		Security:                 b.security,
		Providers:                b.providers,
		Execution:                b.execution,
		Decisions:                b.decisions,
		Events:                   b.events,
		Settings:                 b.settings,
		Resources:                b.startup.resources,
		GitMgr:                   b.git.mgr,
		GitStatusCache:           b.git.status,
		WorkersCfg:               b.worker.cfg,
		TestSecretMatcher:        b.startup.cfg.TestSecretMatcher,
		DecodeCompleteLeg:        decodeCompleteLeg(b),
		EnsureSecretCapabilities: b.wireSecretCapabilities,
	})
	return b.boards.WireBoardAndResearch(b.startup.ctx)
}

func (b *serveBuilder) wireGroundingAndFindings() error {
	return b.boards.WireGroundingAndFindings(b.startup.ctx)
}
