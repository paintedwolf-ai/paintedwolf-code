package sourcecatalog

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIndexFilePagesKeepTheirGenerationAndBoundRows(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	for i := range TreeFilePageLimit + 7 {
		writeTreeTestFile(t, root.Path, fmt.Sprintf("dir/%04d.go", i), "source")
	}
	r := readyIndex(t, c, root)
	first, err := r.FilePage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", TreeFilePageLimit)
	testutil.FailErr(t, "first page", err)
	if len(first) != TreeFilePageLimit || r.RowsRead != TreeFilePageLimit {
		t.Fatalf("page=%d rows=%d", len(first), r.RowsRead)
	}
	writeTreeTestFile(t, root.Path, "dir/9999.go", "added")
	c.InvalidateRoot(root.Path, "dir/9999.go")
	second, err := r.FilePage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, first[len(first)-1].Path, TreeFilePageLimit)
	testutil.FailErr(t, "pinned next page", err)
	if len(second) != 7 || second[0].RootID != root.ID {
		t.Fatalf("next page=%+v", second)
	}
	for i, entry := range append(first, second...) {
		if entry.Path != fmt.Sprintf("dir/%04d.go", i) {
			t.Fatalf("path %d=%s", i, entry.Path)
		}
	}
}

func TestIndexPathCandidatesTreatQueryAsLiteralUnicode(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	for _, path := range []string{"Älpha.SQL", "alpha.sql", "a[b].txt", "a?b.txt", "a*b.txt"} {
		writeTreeTestFile(t, root.Path, path, "source")
	}
	r := readyIndex(t, c, root)
	for query, want := range map[string][]string{"äsql": {"Älpha.SQL"}, "[": {"a[b].txt"}, "?": {"a?b.txt"}, "*": {"a*b.txt"}} {
		var paths []string
		for _, tier := range []FilePathMatch{FileBasenamePrefix, FileBasenameContains, FilePathSubsequence} {
			found, err := r.MatchingFilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, query, tier, "", 10)
			testutil.FailErr(t, "query paths", err)
			paths = append(paths, found...)
		}
		if !slices.Equal(paths, want) {
			t.Errorf("query=%q paths=%v want=%v", query, paths, want)
		}
	}
}

// TestIndexProjectionsShareOneGeneration: quick open and code search read the
// same rows through different predicates.
func TestIndexProjectionsShareOneGeneration(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "src/main.go", "source")
	writeTreeTestFile(t, root.Path, ".github/workflows/ci.yml", "workflow")
	r := readyIndex(t, c, root)

	all, err := r.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", TreeFilePageLimit)
	testutil.FailErr(t, "all files", err)
	if !slices.Equal(all, []string{".github/workflows/ci.yml", "src/main.go"}) {
		t.Fatalf("all=%v", all)
	}
	visible, err := r.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "visible files", err)
	if !slices.Equal(visible, []string{"src/main.go"}) {
		t.Fatalf("visible=%v", visible)
	}
	named, err := r.NamedFiles(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, []string{".github/workflows/ci.yml"})
	testutil.FailErr(t, "named files", err)
	if !slices.Equal(named, []string{".github/workflows/ci.yml"}) {
		t.Fatalf("named=%v", named)
	}
	addresses, err := r.FileAddressPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, 0, "", 10)
	testutil.FailErr(t, "address page", err)
	if !slices.Equal(addresses, []string{"src/main.go", ".github/workflows/ci.yml"}) {
		t.Fatalf("addresses=%v want shortest first", addresses)
	}
}

func TestIndexLiteralPreparationSurvivesSearchCancellation(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.txt", "needle")
	writeTreeTestFile(t, root.Path, "b.txt", "unrelated")
	r := readyIndex(t, c, root)
	entries, err := r.FilePage(t.Context(), FileScope{Audience: HumanAudience}, "", TreeFilePageLimit)
	testutil.FailErr(t, "read metadata", err)
	started, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	query := LiteralQuery{IncludeKey: "test", Require: litprefilter.AnyOf("needle"), Open: func(entry Entry) (io.ReadCloser, error) {
		once.Do(func() { close(started) })
		<-release
		return literalTestOpener(root.Path)(entry)
	}}
	ctx, cancel := context.WithCancel(t.Context())
	page := c.Literals.IndexLiteralCandidates(ctx, r, query, entries)
	if len(page.Candidates) != 2 || !page.Warming {
		t.Fatalf("cold candidates=%d warming=%v", len(page.Candidates), page.Warming)
	}
	<-started
	cancel()
	releaseOnce.Do(func() { close(release) })
	r.store.mu.Lock()
	build := r.store.content[indexLiteralScopeKey(query)]
	r.store.mu.Unlock()
	if build == nil {
		t.Fatal("content preparation lost its scope")
	}
	<-build.done
	page = c.Literals.IndexLiteralCandidates(t.Context(), r, query, entries)
	if page.Warming || !slices.Equal(literalCandidatePaths(page.Candidates), []string{"a.txt"}) {
		t.Fatalf("warm candidates=%v warming=%v", literalCandidatePaths(page.Candidates), page.Warming)
	}
	writeTreeTestFile(t, root.Path, "b.txt", "needle!!!")
	c.InvalidateRoot(root.Path, "b.txt")
	page = c.Literals.IndexLiteralCandidates(t.Context(), r, query, entries)
	if !slices.Equal(literalCandidatePaths(page.Candidates), []string{"a.txt", "b.txt"}) {
		t.Fatalf("stale generation lost changed candidate: %v", literalCandidatePaths(page.Candidates))
	}
	testutil.FailErr(t, "join preparation", c.Drain(context.Background()))
}

func TestIndexLiteralScopesBoundAndDrainQueuedWorkers(t *testing.T) {
	c := treeTestCatalog(t)
	root := Root{ID: "root", Path: t.TempDir()}
	writeTreeTestFile(t, root.Path, "a.txt", "source")
	reader := readyIndex(t, c, root)
	c.Literals.broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceIO: {Total: 1, PerLane: 1}})
	release, err := c.Literals.broker.Acquire(t.Context(), backgroundwork.Request{Lane: root.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceIO}})
	testutil.FailErr(t, "hold content admission", err)
	defer release()
	for i := range literalPreparationScopeCap + 3 {
		c.Literals.prepareIndexLiterals(t.Context(), reader, LiteralQuery{IncludeKey: fmt.Sprint(i), Open: literalTestOpener(root.Path)})
	}
	reader.store.mu.Lock()
	count := len(reader.store.content)
	reader.store.mu.Unlock()
	if count != literalPreparationScopeCap {
		t.Fatalf("active scopes=%d", count)
	}
	testutil.FailErr(t, "drain queued content", c.Drain(t.Context()))
	reader.store.mu.Lock()
	defer reader.store.mu.Unlock()
	for key, build := range reader.store.content {
		select {
		case <-build.done:
		default:
			t.Errorf("scope %s survived drain", key)
		}
	}
}

func TestIndexScopesKeepHumanSearchSeparateFromAgentMetadata(t *testing.T) {
	c, root := indexFixture(t)
	for _, name := range []string{"src/main.go", ".hidden.txt", ".paintedwolf/rules.yaml"} {
		writeIndexFile(t, root.Path, name, "needle")
	}
	reader := waitIndex(t, c, root)
	cases := []struct {
		scope FileScope
		want  []string
	}{
		{FileScope{Audience: HumanAudience, IncludeHidden: true}, []string{".hidden.txt", ".paintedwolf/rules.yaml", "src/main.go"}},
		{FileScope{Audience: AgentAudience, IncludeHidden: true}, []string{".hidden.txt", "src/main.go"}},
		{FileScope{Audience: HumanAudience}, []string{"src/main.go"}},
		{FileScope{Audience: AgentAudience}, []string{"src/main.go"}},
	}
	for _, tc := range cases {
		got, err := reader.FilePathsPage(t.Context(), tc.scope, "", 10)
		testutil.FailErr(t, "read scoped paths", err)
		count, err := reader.FileCount(t.Context(), tc.scope)
		testutil.FailErr(t, "read scoped count", err)
		if !slices.Equal(got, tc.want) || count != len(tc.want) {
			t.Fatalf("scope %+v: paths=%v count=%d want %v", tc.scope, got, count, tc.want)
		}
	}
}

func TestHumanWarmedSharedCatalogNeverLeaksControlPlaneOrKeyMaterialToAgent(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	wtDir := filepath.Join(cfgDir, "worktrees", "wt-1")
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	writeIndexFile(t, home, "project/main.go", "package main // SecretNeedle")
	writeIndexFile(t, home, ".ssh/id_ed25519", "PRIVATE KEY SecretNeedle")
	writeIndexFile(t, cfgDir, "settings.yaml", "api_key: SecretNeedle")
	writeIndexFile(t, cfgDir, "packs/custom.yaml", "rules: [SecretNeedle]")
	writeIndexFile(t, cfgDir, "paintedwolf.db", "sqlite-db")
	writeIndexFile(t, cfgDir, "paintedwolf.db-wal", "sqlite-wal")
	writeIndexFile(t, cfgDir, "paintedwolf.db-shm", "sqlite-shm")
	writeIndexFile(t, cfgDir, "source-catalog.db-wal", "catalog-wal")
	writeIndexFile(t, cfgDir, "store.db-wal", "store-wal")
	writeIndexFile(t, cfgDir, "runtime.json", "{}")
	writeIndexFile(t, cfgDir, "daemon.lock", "lock")
	writeIndexFile(t, cfgDir, "cache/index.bin", "cache-bytes")
	writeIndexFile(t, cfgDir, "sessions/s1.json", "{}")
	writeIndexFile(t, wtDir, "work.go", "package work")

	c := treeTestCatalog(t)

	// 1. Human warms shared catalog on $HOME (strict ancestor of configdir.Root() and ~/.ssh).
	homeRoot := Root{ID: "home", Path: home}
	homeReader := waitIndex(t, c, homeRoot)

	// HumanAudience (IncludeHidden: true) sees ~/.ssh/id_ed25519 and project/main.go,
	// while .paintedwolf (the active control directory) is excluded from parent observation.
	humanHomePaths, err := homeReader.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", 20)
	testutil.FailErr(t, "human home paths", err)
	if !slices.Equal(humanHomePaths, []string{".ssh/id_ed25519", "project/main.go"}) {
		t.Fatalf("human home paths = %v, want [.ssh/id_ed25519 project/main.go]", humanHomePaths)
	}

	// Simulate a pre-existing cached row in SQLite nodes for .paintedwolf/settings.yaml
	// (with agent_metadata=0 to prove we do not rely solely on agent_metadata=1).
	_ = homeReader.Close()
	sHome, err := c.Trees.indexStore(t.Context(), "p", homeRoot)
	testutil.FailErr(t, "open home indexStore", err)
	rawDB, err := openTreeDB(t.Context(), sHome.file)
	testutil.FailErr(t, "open raw home db", err)
	_, err = rawDB.ExecContext(t.Context(),
		`INSERT OR REPLACE INTO nodes(path,search_path,search_name,parent,name,depth,indexed,agent_metadata,first_listed,is_dir,symlink,regular,hidden,size,mode,modified,refused)
		 VALUES('.paintedwolf/settings.yaml','.paintedwolf/settings.yaml','settings.yaml','.paintedwolf','settings.yaml',2,1,0,1,0,0,1,1,24,420,1,'')`)
	testutil.FailErr(t, "inject pre-existing cached control-plane row", err)
	_ = rawDB.Close()

	homeReader, _, err = c.Trees.OpenIndex(t.Context(), "p", homeRoot, time.Second)
	testutil.FailErr(t, "reopen home index", err)
	t.Cleanup(func() { _ = homeReader.Close() })

	// AgentAudience (both IncludeHidden: true and false) must see ONLY project/main.go:
	// zero .ssh/id_ed25519 and zero .paintedwolf/settings.yaml across counts, pages, search, and named files.
	for _, agentScope := range []FileScope{
		{Audience: AgentAudience, IncludeHidden: true},
		{Audience: AgentAudience, IncludeHidden: false},
	} {
		count, err := homeReader.FileCount(t.Context(), agentScope)
		testutil.FailErr(t, "agent home count", err)
		if count != 1 {
			t.Fatalf("scope %+v: agent home count = %d, want 1", agentScope, count)
		}
		page, err := homeReader.FilePage(t.Context(), agentScope, "", 20)
		testutil.FailErr(t, "agent home FilePage", err)
		if len(page) != 1 || page[0].Path != "project/main.go" {
			t.Fatalf("scope %+v: agent home FilePage = %+v, want [project/main.go]", agentScope, page)
		}
		paths, err := homeReader.FilePathsPage(t.Context(), agentScope, "", 20)
		testutil.FailErr(t, "agent home FilePathsPage", err)
		if !slices.Equal(paths, []string{"project/main.go"}) {
			t.Fatalf("scope %+v: agent home FilePathsPage = %v, want [project/main.go]", agentScope, paths)
		}
		litSSH, err := homeReader.LiteralFilePathsPage(t.Context(), agentScope, []string{"id_ed25519"}, false, "", 20)
		testutil.FailErr(t, "agent home LiteralFilePathsPage ssh", err)
		if len(litSSH) != 0 {
			t.Fatalf("scope %+v: agent leaked .ssh via LiteralFilePathsPage: %v", agentScope, litSSH)
		}
		litCfg, err := homeReader.LiteralFilePathsPage(t.Context(), agentScope, []string{"settings.yaml"}, false, "", 20)
		testutil.FailErr(t, "agent home LiteralFilePathsPage cfg", err)
		if len(litCfg) != 0 {
			t.Fatalf("scope %+v: agent leaked control-plane row via LiteralFilePathsPage: %v", agentScope, litCfg)
		}
		addr, err := homeReader.FileAddressPage(t.Context(), agentScope, 0, "", 20)
		testutil.FailErr(t, "agent home FileAddressPage", err)
		if !slices.Equal(addr, []string{"project/main.go"}) {
			t.Fatalf("scope %+v: agent home FileAddressPage = %v, want [project/main.go]", agentScope, addr)
		}
		for _, q := range []string{"id_ed25519", "settings"} {
			for _, tier := range []FilePathMatch{FileBasenamePrefix, FileBasenameContains, FilePathSubsequence} {
				matched, err := homeReader.MatchingFilePathsPage(t.Context(), agentScope, q, tier, "", 20)
				testutil.FailErr(t, "agent home MatchingFilePathsPage", err)
				if len(matched) != 0 {
					t.Fatalf("scope %+v query=%q tier=%v: agent leaked %v", agentScope, q, tier, matched)
				}
			}
		}
		named, err := homeReader.NamedFiles(t.Context(), agentScope, []string{".ssh/id_ed25519", ".paintedwolf/settings.yaml", "project/main.go"})
		testutil.FailErr(t, "agent home NamedFiles", err)
		if !slices.Equal(named, []string{"project/main.go"}) {
			t.Fatalf("scope %+v: agent home NamedFiles = %v, want [project/main.go]", agentScope, named)
		}
	}

	// Orientation summary on $HOME must also exclude .ssh and .paintedwolf.
	homeSummary, status, err := c.Trees.OpenSummary(t.Context(), "proj", homeRoot, TreeScope{Key: "agent-default"}, 5*time.Second)
	testutil.FailErr(t, "open home summary", err)
	if status.State != StateReady || homeSummary == nil {
		t.Fatalf("home summary state=%v reader=%v", status, homeSummary)
	}
	t.Cleanup(func() { _ = homeSummary.Close() })
	rootNode, err := homeSummary.Node(t.Context(), ".")
	testutil.FailErr(t, "read home summary root", err)
	if rootNode.Files != 1 {
		t.Fatalf("home summary root Files = %d, want 1 (project/main.go only)", rootNode.Files)
	}

	// 2. Human warms shared catalog on directly attached configdir.Root() (cfgDir).
	cfgRoot := Root{ID: "cfg", Path: cfgDir}
	cfgReader := waitIndex(t, c, cfgRoot)

	// HumanAudience on directly attached configdir.Root() sees human-authored config (packs/custom.yaml, settings.yaml)
	// and ZERO runtime DB/WAL/SHM/cache/session/worktree files.
	humanCfgPaths, err := cfgReader.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", 20)
	testutil.FailErr(t, "human cfg paths", err)
	if !slices.Equal(humanCfgPaths, []string{"packs/custom.yaml", "settings.yaml"}) {
		t.Fatalf("human cfg paths = %v, want [packs/custom.yaml settings.yaml]", humanCfgPaths)
	}
	humanCfgCount, err := cfgReader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "human cfg count", err)
	if humanCfgCount != 2 {
		t.Fatalf("human cfg count = %d, want 2", humanCfgCount)
	}

	// AgentAudience on the same human-warmed cfgReader must get 0 files and empty pages/search/named results.
	for _, agentScope := range []FileScope{
		{Audience: AgentAudience, IncludeHidden: true},
		{Audience: AgentAudience, IncludeHidden: false},
	} {
		count, err := cfgReader.FileCount(t.Context(), agentScope)
		testutil.FailErr(t, "agent cfg count", err)
		if count != 0 {
			t.Fatalf("scope %+v: agent cfg count = %d, want 0", agentScope, count)
		}
		page, err := cfgReader.FilePage(t.Context(), agentScope, "", 20)
		testutil.FailErr(t, "agent cfg FilePage", err)
		if len(page) != 0 {
			t.Fatalf("scope %+v: agent cfg FilePage = %+v, want empty", agentScope, page)
		}
		paths, err := cfgReader.FilePathsPage(t.Context(), agentScope, "", 20)
		testutil.FailErr(t, "agent cfg FilePathsPage", err)
		if len(paths) != 0 {
			t.Fatalf("scope %+v: agent cfg FilePathsPage = %v, want empty", agentScope, paths)
		}
		named, err := cfgReader.NamedFiles(t.Context(), agentScope, []string{"settings.yaml", "packs/custom.yaml"})
		testutil.FailErr(t, "agent cfg NamedFiles", err)
		if len(named) != 0 {
			t.Fatalf("scope %+v: agent cfg NamedFiles = %v, want empty", agentScope, named)
		}
	}

	// Orientation summary on directly attached configdir.Root() has 0 files for AgentAudience.
	cfgSummary, cfgSumStatus, err := c.Trees.OpenSummary(t.Context(), "proj", cfgRoot, TreeScope{Key: "agent-default"}, 5*time.Second)
	testutil.FailErr(t, "open cfg summary", err)
	if cfgSumStatus.State != StateReady || cfgSummary == nil {
		t.Fatalf("cfg summary state=%v reader=%v", cfgSumStatus, cfgSummary)
	}
	t.Cleanup(func() { _ = cfgSummary.Close() })
	cfgRootNode, err := cfgSummary.Node(t.Context(), ".")
	testutil.FailErr(t, "read cfg summary root", err)
	if cfgRootNode.Files != 0 || cfgRootNode.Children != 0 {
		t.Fatalf("cfg summary root = %+v, want 0 files and 0 children", cfgRootNode)
	}

	// 3. Explicit managed workspace (<configdir>/worktrees/wt-1) remains accessible to both HumanAudience and AgentAudience.
	wtRoot := Root{ID: "wt", Path: wtDir}
	wtReader := waitIndex(t, c, wtRoot)
	for _, scope := range []FileScope{
		{Audience: HumanAudience, IncludeHidden: true},
		{Audience: AgentAudience, IncludeHidden: true},
		{Audience: AgentAudience, IncludeHidden: false},
	} {
		paths, err := wtReader.FilePathsPage(t.Context(), scope, "", 20)
		testutil.FailErr(t, "wt FilePathsPage", err)
		count, err := wtReader.FileCount(t.Context(), scope)
		testutil.FailErr(t, "wt FileCount", err)
		if !slices.Equal(paths, []string{"work.go"}) || count != 1 {
			t.Fatalf("scope %+v: managed worktree paths=%v count=%d, want [work.go] and 1", scope, paths, count)
		}
	}
}
