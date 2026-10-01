package benchfixtures

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestGoFullParseFixturesLoadAuthenticatedSnapshots(t *testing.T) {
	fixtures, err := LoadGoFullParseFixtures()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(fixtures), 4; got != want {
		t.Fatalf("fixture count=%d want=%d", got, want)
	}
	wantIDs := []string{"query_compile", "rewrite", "language", "grammargen_lr"}
	for i, loaded := range fixtures {
		if loaded.Fixture.ID != wantIDs[i] {
			t.Fatalf("fixture[%d].ID=%q want=%q", i, loaded.Fixture.ID, wantIDs[i])
		}
		if err := loaded.Fixture.VerifySource(loaded.Source); err != nil {
			t.Fatalf("fixture[%d]: %v", i, err)
		}
		if loaded.Fixture.OriginRevision != GoFixtureOriginRevision {
			t.Fatalf("fixture[%d].OriginRevision=%q want=%q", i, loaded.Fixture.OriginRevision, GoFixtureOriginRevision)
		}
	}
}

func TestFixtureAdmissionFailsClosed(t *testing.T) {
	fixture := GoFullParseFixtures()[0]
	source, err := fixture.Load()
	if err != nil {
		t.Fatal(err)
	}

	corruptSource := append([]byte(nil), source...)
	corruptSource[len(corruptSource)/2] ^= 1
	if err := fixture.VerifySource(corruptSource); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("corrupt source error=%v, want sha256 failure", err)
	}

	corruptMetadata := fixture
	corruptMetadata.CompressedSHA256 = strings.Repeat("0", 64)
	if _, err := corruptMetadata.Load(); err == nil || !strings.Contains(err.Error(), "compressed sha256") {
		t.Fatalf("corrupt metadata error=%v, want compressed sha256 failure", err)
	}
	if err := fixture.VerifyDeepTreeDigest("deadbeef"); err == nil || !strings.Contains(err.Error(), "deep tree sha256") {
		t.Fatalf("corrupt deep digest error=%v, want deep tree sha256 failure", err)
	}
	corruptMetadata = fixture
	corruptMetadata.WorkloadIdentity = WorkloadIdentity{}
	if _, err := corruptMetadata.Load(); err == nil || !strings.Contains(err.Error(), "workload identity") {
		t.Fatalf("missing workload identity error=%v, want workload identity failure", err)
	}
}

func TestFixtureWorkloadIdentityFailsClosed(t *testing.T) {
	fixture := GoFullParseFixtures()[0]
	expect := fixture.WorkloadIdentity
	runtime := gotreesitter.ParseRuntime{
		MaxStacksSeen:        expect.MinMaxStacksSeen,
		MultiStackIterations: expect.MinMultiStackIterations,
		MultiStackTokens:     expect.MinMultiStackTokens,
	}
	coverage := admittedFixtureNodeKindCoverage()
	if err := fixture.VerifyWorkloadIdentity(runtime, coverage); err != nil {
		t.Fatalf("verified workload identity: %v", err)
	}

	for _, tc := range []struct {
		name    string
		runtime gotreesitter.ParseRuntime
		want    string
	}{
		{name: "straight LR", runtime: gotreesitter.ParseRuntime{MaxStacksSeen: 1, MultiStackIterations: expect.MinMultiStackIterations, MultiStackTokens: expect.MinMultiStackTokens}, want: "max stacks"},
		{name: "few multi iterations", runtime: gotreesitter.ParseRuntime{MaxStacksSeen: expect.MinMaxStacksSeen, MultiStackIterations: expect.MinMultiStackIterations - 1, MultiStackTokens: expect.MinMultiStackTokens}, want: "multi-stack iterations"},
		{name: "few multi tokens", runtime: gotreesitter.ParseRuntime{MaxStacksSeen: expect.MinMaxStacksSeen, MultiStackIterations: expect.MinMultiStackIterations, MultiStackTokens: expect.MinMultiStackTokens - 1}, want: "multi-stack tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := fixture.VerifyRuntimeIdentity(tc.runtime); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q failure", err, tc.want)
			}
		})
	}

	delete(coverage, "comment")
	if err := fixture.VerifyNodeKindCoverage(coverage); err == nil || !strings.Contains(err.Error(), "comment") {
		t.Fatalf("error=%v, want missing comment failure", err)
	}
}

func TestGoFullParseSuiteNodeKindCoverageFailsClosed(t *testing.T) {
	coverage := admittedFixtureNodeKindCoverage()
	for _, kind := range goSuiteRequiredNodeKinds {
		coverage[kind] = 1
	}
	coverage[goSuiteRequiredAnyNodeKinds[0]] = 1
	if err := VerifyGoFullParseSuiteNodeKindCoverage(coverage); err != nil {
		t.Fatalf("verified suite coverage: %v", err)
	}

	delete(coverage, "qualified_type")
	if err := VerifyGoFullParseSuiteNodeKindCoverage(coverage); err == nil || !strings.Contains(err.Error(), "qualified_type") {
		t.Fatalf("error=%v, want missing qualified_type failure", err)
	}
	coverage["qualified_type"] = 1
	delete(coverage, goSuiteRequiredAnyNodeKinds[0])
	if err := VerifyGoFullParseSuiteNodeKindCoverage(coverage); err == nil || !strings.Contains(err.Error(), "neither") {
		t.Fatalf("error=%v, want missing switch/select failure", err)
	}
}

func admittedFixtureNodeKindCoverage() NodeKindCoverage {
	coverage := make(NodeKindCoverage)
	for _, kind := range goFixtureRequiredNodeKinds {
		coverage[kind] = 1
	}
	return coverage
}

func TestVerifyGoGrammarIdentityFailsClosed(t *testing.T) {
	if err := VerifyGoGrammarIdentity(GoGrammarCommit, GoGrammarBlobSHA256); err != nil {
		t.Fatalf("verified identity: %v", err)
	}
	for _, tc := range []struct {
		name   string
		commit string
		blob   string
	}{
		{name: "missing", commit: "", blob: ""},
		{name: "wrong commit", commit: "deadbeef", blob: GoGrammarBlobSHA256},
		{name: "wrong blob", commit: GoGrammarCommit, blob: "deadbeef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyGoGrammarIdentity(tc.commit, tc.blob); err == nil {
				t.Fatal("identity unexpectedly admitted")
			}
		})
	}
}
