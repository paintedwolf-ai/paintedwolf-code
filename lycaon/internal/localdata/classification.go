package localdata

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/clisocket"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostidentity"
)

// EntryClass is the lifecycle class of an entry under the engine config root.
type EntryClass string

const (
	// ClassDurableFile survives local data clears.
	ClassDurableFile EntryClass = "durable_file"
	// ClassDurableDir survives local data clears and is archived per file.
	ClassDurableDir EntryClass = "durable_dir"
	// ClassArchiveExcluded survives local data clears but stays out of backups.
	ClassArchiveExcluded EntryClass = "archive_excluded"
	// ClassLocalDataBucket is rebuildable state cleared through a local-data bucket.
	ClassLocalDataBucket EntryClass = "local_data_bucket"
	// ClassStoreCoupled is rebuildable state keyed by store.db identities.
	ClassStoreCoupled EntryClass = "store_coupled"
	// ClassRuntime is live process state such as locks and host mirrors.
	ClassRuntime EntryClass = "runtime"
)

// ConfigRootEntry classifies one top-level entry under the config root.
type ConfigRootEntry struct {
	Name                 string
	IsDir                bool
	Class                EntryClass
	IsSecret             bool
	IncludeInBackup      bool
	IncludeInDiagnostics bool
	// StoreCoupled entries are removed when store.db is reset or replaced.
	StoreCoupled bool
	// Bucket is the local-data bucket that measures and clears the entry.
	Bucket string
	Reason string
}

const (
	storeFileName   = "store.db"
	appStateDirName = "app-state-v1"
)

// sqliteJournalSuffixes name the files SQLite keeps beside a database.
var sqliteJournalSuffixes = []string{"-wal", "-shm", "-journal"}

var configRootRegistry = []ConfigRootEntry{
	// Durable files
	{Name: storeFileName, IsDir: false, Class: ClassDurableFile, StoreCoupled: true, IncludeInBackup: true, Reason: "primary durable SQLite database"},
	{Name: storeFileName + "-wal", IsDir: false, Class: ClassDurableFile, StoreCoupled: true, Reason: "live SQLite write-ahead log for store.db"},
	{Name: storeFileName + "-shm", IsDir: false, Class: ClassDurableFile, StoreCoupled: true, Reason: "live SQLite shared memory index for store.db"},
	{Name: storeFileName + "-journal", IsDir: false, Class: ClassDurableFile, StoreCoupled: true, Reason: "live SQLite rollback journal for store.db"},
	{Name: "providers.local.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, Reason: "local model provider configurations"},
	{Name: "model-policy.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "model selection and routing policy"},
	{Name: "settings.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "general device settings override"},
	{Name: "approvals.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "user-granted tool and command approval rules"},
	{Name: "limits.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "execution limits and resource budgets"},
	{Name: "pricing.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "device pricing model parameters"},
	{Name: "security-scanners.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "device security scanner configuration"},
	{Name: "scanners.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "custom external scanner definitions"},
	{Name: "scan-hints.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "per-path scanner dispatch hints"},
	{Name: "logview.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "log viewing and tail preferences"},
	{Name: "review.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "code review workflow preferences"},
	{Name: "trust-surfaces.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "trusted repository root declarations"},
	{Name: "file-summaries.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "file summary model preferences"},
	{Name: "power.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "engine power and concurrency policies"},
	{Name: "history-retention.json", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "history prune policies and schedules"},
	{Name: "web-research-config.yaml", IsDir: false, Class: ClassDurableFile, IncludeInBackup: true, IncludeInDiagnostics: true, Reason: "user web research preferences and provider selections"},

	// Durable directories
	{Name: appStateDirName, IsDir: true, Class: ClassDurableDir, IncludeInBackup: true, Reason: "application preferences and persisted client states"},
	{Name: editoroutbox.Directory(), IsDir: true, Class: ClassDurableDir, StoreCoupled: true, IncludeInBackup: true, Reason: "uncommitted editor outbox transactions and recovery frames"},
	{Name: "projects", IsDir: true, Class: ClassDurableDir, StoreCoupled: true, IncludeInBackup: true, Reason: "per-project durable state and metadata"},
	{Name: enginepaths.SourceContentDirName, IsDir: true, Class: ClassDurableDir, StoreCoupled: true, IncludeInBackup: true, Reason: "content-addressed source blob storage"},
	{Name: enginepaths.WorkerBaselinesDirName, IsDir: true, Class: ClassDurableDir, StoreCoupled: true, IncludeInBackup: true, Reason: "worker baseline source snapshots"},
	{Name: enginepaths.DraftsDirName, IsDir: true, Class: ClassDurableDir, StoreCoupled: true, IncludeInBackup: true, Reason: "user-drafted projects and uncommitted initializations"},
	{Name: enginepaths.SessionCheckpointsDirName, IsDir: true, Class: ClassDurableDir, StoreCoupled: true, IncludeInBackup: true, Reason: "pre-turn session recovery checkpoints"},

	// Durable but outside backups
	{Name: credentialstore.VaultBasename, IsDir: false, Class: ClassArchiveExcluded, IsSecret: true, Reason: "age-encrypted secret credential vault"},
	{Name: credentialstore.IdentityBasename, IsDir: false, Class: ClassArchiveExcluded, IsSecret: true, Reason: "passphrase-wrapped vault identity for Linux/Windows"},
	{Name: credentialstore.DevelopmentIdentityBasename, IsDir: false, Class: ClassArchiveExcluded, IsSecret: true, Reason: "plain identity file used only in development"},
	{Name: "api.token", IsDir: false, Class: ClassArchiveExcluded, IsSecret: true, Reason: "local bearer token authorizing sidecar API calls"},
	{Name: hostidentity.FileName, IsDir: false, Class: ClassArchiveExcluded, IsSecret: true, Reason: "local host identity private key"},
	{Name: "daemon.json", IsDir: false, Class: ClassArchiveExcluded, Reason: "local host process supervisor metadata"},
	{Name: "mcp.yaml", IsDir: false, Class: ClassArchiveExcluded, IsSecret: true, Reason: "MCP server definitions which may contain auth tokens/headers"},
	{Name: extpacks.DeviceDesiredName, IsDir: false, Class: ClassArchiveExcluded, IncludeInDiagnostics: true, Reason: "device-specific installed extension pack list"},
	{Name: extpacks.DeviceLockName, IsDir: false, Class: ClassArchiveExcluded, IncludeInDiagnostics: true, Reason: "lockfile pinning locally installed extension versions"},
	{Name: db.UpgradeRecoveryDirName, IsDir: true, Class: ClassArchiveExcluded, Reason: "pre-upgrade recovery snapshots preserved outside backups"},

	// Rebuildable state keyed by store.db identities
	{Name: webIndexFileName, IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketWebIndex, Reason: "rebuildable personal web index SQLite database"},
	{Name: webIndexFileName + "-wal", IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketWebIndex, Reason: "SQLite write-ahead log for web-index.db"},
	{Name: webIndexFileName + "-shm", IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketWebIndex, Reason: "SQLite shared memory index for web-index.db"},
	{Name: webIndexFileName + "-journal", IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketWebIndex, Reason: "SQLite rollback journal for web-index.db"},
	{Name: enginepaths.SourceObservationsDBName, IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketSourceObservations, Reason: "rebuildable source observation index SQLite database"},
	{Name: enginepaths.SourceObservationsDBName + "-wal", IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketSourceObservations, Reason: "SQLite write-ahead log for source-observations.db"},
	{Name: enginepaths.SourceObservationsDBName + "-shm", IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketSourceObservations, Reason: "SQLite shared memory index for source-observations.db"},
	{Name: enginepaths.SourceObservationsDBName + "-journal", IsDir: false, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketSourceObservations, Reason: "SQLite rollback journal for source-observations.db"},
	{Name: enginepaths.WorkerBranchesDirName, IsDir: true, Class: ClassStoreCoupled, StoreCoupled: true, IncludeInBackup: true, Bucket: BucketWorkerBranches, Reason: "write-worker branch worktrees; backed up per-file"},
	{Name: enginepaths.WorkerSeedsDirName, IsDir: true, Class: ClassStoreCoupled, StoreCoupled: true, Reason: "rebuildable bridge generation cache for copy-on-write worker branches"},
	{Name: enginepaths.SessionWorktreesDirName, IsDir: true, Class: ClassStoreCoupled, StoreCoupled: true, Reason: "session worktrees outside project roots"},
	{Name: enginepaths.ScratchDirName, IsDir: true, Class: ClassStoreCoupled, StoreCoupled: true, Bucket: BucketSessionScratch, Reason: "per-session scratch folders keyed by session id; clearable chat working data that nothing rebuilds"},

	// Rebuildable caches cleared through local-data buckets
	{Name: enginepaths.BrowserCacheDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketBrowserCache, Reason: "rebuildable managed browser cache"},
	{Name: enginepaths.CacheDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketSourceCatalog, Reason: "rebuildable subtree grouping sourcecatalog caches"},
	{Name: "debug", IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketDebugLogs, Reason: "diagnostic debug traces and session captures"},
	{Name: enginepaths.ExtensionsCacheDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketExtensionCache, Reason: "content-addressed extension pack tarball cache"},
	{Name: enginepaths.ExtensionsMetaDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketExtensionCache, Reason: "community meta-pack index cache"},
	{Name: enginepaths.FetchCacheDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketFetchCache, Reason: "web research HTTP response cache"},
	{Name: enginepaths.ModelfeedDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketModelfeed, Reason: "downloaded provider model metadata cache"},
	{Name: "osv-cache", IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketOSVCache, Reason: "downloaded OSV vulnerability databases"},
	{Name: enginepaths.PricingCacheDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketPricingCache, Reason: "downloaded provider pricing data cache"},
	{Name: enginepaths.RepoOrientationDirName, IsDir: true, Class: ClassLocalDataBucket, Bucket: BucketSourceObservations, Reason: "rebuildable repo orientation observations"},
	{Name: enginepaths.VerifyDetectFileName, IsDir: false, Class: ClassLocalDataBucket, Bucket: BucketScanScratch, Reason: "detected verify commands cache file"},

	// Runtime
	{Name: clisocket.CompletionCacheName, IsDir: false, Class: ClassRuntime, Reason: "project names cache for CLI shell completion, rewritten as projects change"},
	{Name: enginepaths.SSHDirName, IsDir: true, Class: ClassRuntime, Reason: "host-managed SSH known_hosts mirror for confined subprocesses"},
	{Name: "locks", IsDir: true, Class: ClassRuntime, Reason: "advisory process file locks for extension installations"},
}

// AllClassifiedEntries returns a copy of every classified config-root entry.
func AllClassifiedEntries() []ConfigRootEntry {
	return append([]ConfigRootEntry(nil), configRootRegistry...)
}

// LookupConfigRootEntry classifies name by its base name.
func LookupConfigRootEntry(name string) (ConfigRootEntry, bool) {
	base := filepath.Base(filepath.Clean(name))
	for _, e := range configRootRegistry {
		if e.Name == base {
			return e, true
		}
	}
	return ConfigRootEntry{}, false
}

// entryNames returns the sorted names of registry entries that match.
func entryNames(match func(ConfigRootEntry) bool) []string {
	var out []string
	for _, e := range configRootRegistry {
		if match(e) {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// isStoreFile reports store.db and its journals, which db.RemoveStore owns.
func isStoreFile(name string) bool {
	rest, ok := strings.CutPrefix(name, storeFileName)
	return ok && (rest == "" || slices.Contains(sqliteJournalSuffixes, rest))
}

// IsDiagnosticsAllowed reports whether a config-root file may enter a diagnostics bundle.
func IsDiagnosticsAllowed(name string) bool {
	entry, ok := LookupConfigRootEntry(name)
	if !ok {
		return false
	}
	return entry.IncludeInDiagnostics && !entry.IsSecret
}
