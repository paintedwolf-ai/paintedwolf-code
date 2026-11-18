// Package localdatatest defines independent config-root fixtures.
package localdatatest

type Entry struct {
	Name        string
	IsDir       bool
	Preserve    bool
	IsSecret    bool
	Backup      bool
	Diagnostics bool
}

// Entries pins the config-root paths exercised by persistence and exclusion tests.
func Entries() []Entry {
	return []Entry{
		{Name: "store.db.integrity-failure.json", Preserve: true, Diagnostics: true},
		{Name: "store.db", Preserve: true, Backup: true},
		{Name: "store.db-wal", Preserve: true},
		{Name: "store.db-shm", Preserve: true},
		{Name: "store.db-journal", Preserve: true},
		{Name: "providers.local.yaml", Preserve: true, Backup: true},
		{Name: "model-policy.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "settings.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "approvals.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "command-programs.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "limits.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "pricing.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "security-scanners.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "scanners.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "scan-hints.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "logview.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "review.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "trust-surfaces.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "file-summaries.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "power.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "history-retention.json", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "web-research-config.yaml", Preserve: true, Diagnostics: true, Backup: true},
		{Name: "app-state-v1", IsDir: true, Preserve: true, Backup: true},
		{Name: "document-outbox", IsDir: true, Preserve: true, Backup: true},
		{Name: "projects", IsDir: true, Preserve: true, Backup: true},
		{Name: "source-content", IsDir: true, Preserve: true, Backup: true},
		{Name: "worker-baselines", IsDir: true, Preserve: true, Backup: true},
		{Name: "drafts", IsDir: true, Preserve: true, Backup: true},
		{Name: "session-checkpoints", IsDir: true, Preserve: true, Backup: true},
		{Name: "credential-vault.age", Preserve: true, IsSecret: true},
		{Name: "credential-vault-identity.age", Preserve: true, IsSecret: true},
		{Name: ".credential-vault-development-identity", Preserve: true, IsSecret: true},
		{Name: "api.token", Preserve: true, IsSecret: true},
		{Name: "host-identity.pem", Preserve: true, IsSecret: true},
		{Name: "daemon.json", Preserve: true},
		{Name: "mcp.yaml", Preserve: true, IsSecret: true},
		{Name: "extensions.yaml", Preserve: true, Diagnostics: true},
		{Name: "extensions.lock.yaml", Preserve: true, Diagnostics: true},
		{Name: "upgrade-recovery", IsDir: true, Preserve: true},
		{Name: "local-models", IsDir: true, Preserve: true},
		{Name: "worker-placements.json", Preserve: true},
		{Name: "store.db.upgrade.json", Preserve: true},
		{Name: "web-index.db"},
		{Name: "web-index.db-wal"},
		{Name: "web-index.db-shm"},
		{Name: "web-index.db-journal"},
		{Name: "source-observations.db"},
		{Name: "source-observations.db-wal"},
		{Name: "source-observations.db-shm"},
		{Name: "source-observations.db-journal"},
		{Name: "worker-branches", IsDir: true, Backup: true},
		{Name: "worker-recovery", IsDir: true, Backup: true},
		{Name: "worker-seeds", IsDir: true},
		{Name: "session-worktrees", IsDir: true},
		{Name: "scratch", IsDir: true},
		{Name: "browser-cache", IsDir: true},
		{Name: "cache", IsDir: true},
		{Name: "debug", IsDir: true},
		{Name: "extensions", IsDir: true},
		{Name: "extensions-meta", IsDir: true},
		{Name: "fetch-cache", IsDir: true},
		{Name: "modelfeed", IsDir: true},
		{Name: "osv-cache", IsDir: true},
		{Name: "pricing-cache", IsDir: true},
		{Name: "repo-orientation", IsDir: true},
		{Name: "verify-detect.json"},
		{Name: "cli-projects.cache"},
		{Name: "ssh", IsDir: true},
		{Name: "locks", IsDir: true},
	}
}
