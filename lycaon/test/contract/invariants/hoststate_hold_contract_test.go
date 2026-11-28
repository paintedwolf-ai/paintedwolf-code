package invariants_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
)

var hoststateHoldTree = map[string]string{
	"lycaon/test/contract/internal/hoststate/oracle.go":              "package hoststate\n\nimport \"errors\"\n\nvar ErrAuthorityMemosNotHeld = errors.New(\"not held\")\n\nfunc HoldAuthorityMemos() func() { return func() {} }\nfunc FreezeTurnAuthority() error { return nil }\nfunc FreezeWorkerAuthority() error { return nil }\nfunc WorkerStates() error { return nil }\n",
	"lycaon/test/contract/internal/hoststate/authority_hold_test.go": "package hoststate_test\n\nimport (\n\t\"errors\"\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestAuthorityAccessorsRequireHold(t *testing.T) {\n\tif err := hoststate.FreezeTurnAuthority(); !errors.Is(err, hoststate.ErrAuthorityMemosNotHeld) {\n\t\tt.Fatalf(\"err = %v, want ErrAuthorityMemosNotHeld\", err)\n\t}\n}\n",
	"lycaon/test/contract/sample/sample_test.go":                     "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestHeld(t *testing.T) {\n\tt.Parallel()\n\tt.Cleanup(hoststate.HoldAuthorityMemos())\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
}

// Every test or fuzz entry point that reaches a hoststate authority accessor
// must cover the call with a verified HoldAuthorityMemos acquisition and
// cleanup pattern. Missing holds (even alongside a parallel held sibling),
// discarded releases, accessors called before hold or after release, sibling
// closure holds, deferred TestMain holds skipped by os.Exit, and blank-assigned
// error sentinels are all rejected.
func TestHostStateAuthorityTestsHoldMemos(t *testing.T) {
	t.Parallel()
	holdRule := invariantstest.Rule(t, forbiddencalls.Family, "hoststate-authority-tests-hold-memos")

	for name, tc := range map[string]struct {
		overrides map[string]string
		want      invariantstest.Outcome
		wantPos   string
	}{
		"clean caller-owned hold": {want: invariantstest.Passes},
		"missing hold alongside parallel held sibling": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestParallelHolder(t *testing.T) {\n\tt.Parallel()\n\tt.Cleanup(hoststate.HoldAuthorityMemos())\n\t_ = hoststate.FreezeTurnAuthority()\n}\n\nfunc TestMissingHold(t *testing.T) {\n\tt.Parallel()\n\t_ = hoststate.FreezeWorkerAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:15:",
		},
		"missing hold reached through unheld helper with aliased import": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\ths \"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc unheldHelper() {\n\t_ = hs.WorkerStates()\n}\n\nfunc TestCallsUnheldHelper(t *testing.T) {\n\tunheldHelper()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:13:",
		},
		"discarded release does not satisfy hold": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestDiscardedRelease(t *testing.T) {\n\t_ = hoststate.HoldAuthorityMemos()\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"accessor called before hold acquisition": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestAccessorBeforeHold(t *testing.T) {\n\t_ = hoststate.FreezeTurnAuthority()\n\tt.Cleanup(hoststate.HoldAuthorityMemos())\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"accessor called after explicit release": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestAccessorAfterRelease(t *testing.T) {\n\trelease := hoststate.HoldAuthorityMemos()\n\t_ = hoststate.FreezeTurnAuthority()\n\trelease()\n\t_ = hoststate.FreezeWorkerAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"hold in sibling subtest closure does not cover another subtest": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestSiblingClosureHold(t *testing.T) {\n\tt.Run(\"held\", func(t *testing.T) {\n\t\tt.Cleanup(hoststate.HoldAuthorityMemos())\n\t\t_ = hoststate.FreezeTurnAuthority()\n\t})\n\tt.Run(\"unheld\", func(t *testing.T) {\n\t\t_ = hoststate.FreezeWorkerAuthority()\n\t})\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"blank-assigned error sentinel does not exempt test": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestBlankSentinel(t *testing.T) {\n\t_ = hoststate.ErrAuthorityMemosNotHeld\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"TestMain defer before os.Exit is rejected": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"os\"\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestMain(m *testing.M) {\n\tdefer hoststate.HoldAuthorityMemos()()\n\tos.Exit(m.Run())\n}\n\nfunc TestUsesBrokenTestMain(t *testing.T) {\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:15:",
		},
		"misleading same-name Cleanup receiver is rejected": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\ntype fakeCleaner struct{}\n\nfunc (fakeCleaner) Cleanup(fn func()) { fn() }\n\nfunc TestMisleadingCleanup(t *testing.T) {\n\tvar fake fakeCleaner\n\tfake.Cleanup(hoststate.HoldAuthorityMemos())\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:13:",
		},
		"defer release followed by early explicit release before accessor": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestDeferThenEarlyRelease(t *testing.T) {\n\trelease := hoststate.HoldAuthorityMemos()\n\tdefer release()\n\trelease()\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:9:",
		},
		"TestMain uninvoked closure calling m.Run is rejected": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"os\"\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestMain(m *testing.M) {\n\trelease := hoststate.HoldAuthorityMemos()\n\t_ = func() int { return m.Run() }\n\trelease()\n\tos.Exit(m.Run())\n}\n\nfunc TestUsesClosureTestMain(t *testing.T) {\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:17:",
		},
		"TestMain non-m receiver Run call is rejected": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"os\"\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\ntype fakeRunner struct{}\n\nfunc (fakeRunner) Run() int { return 0 }\n\nfunc TestMain(m *testing.M) {\n\tvar other fakeRunner\n\trelease := hoststate.HoldAuthorityMemos()\n\t_ = other.Run()\n\trelease()\n\tos.Exit(m.Run())\n}\n\nfunc TestUsesFakeRunTestMain(t *testing.T) {\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want:    invariantstest.Violates,
			wantPos: "test/contract/sample/sample_test.go:22:",
		},
		"valid caller-owned hold calling unheld helper": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc freezeHelper() {\n\t_ = hoststate.FreezeTurnAuthority()\n}\n\nfunc TestCallerOwnsHold(t *testing.T) {\n\tt.Cleanup(hoststate.HoldAuthorityMemos())\n\tfreezeHelper()\n}\n",
			},
			want: invariantstest.Passes,
		},
		"valid delegated helper owning its own hold": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc delegatedHelper() {\n\tdefer hoststate.HoldAuthorityMemos()()\n\t_ = hoststate.FreezeWorkerAuthority()\n}\n\nfunc TestDelegatesHold(t *testing.T) {\n\tdelegatedHelper()\n}\n",
			},
			want: invariantstest.Passes,
		},
		"valid explicit release after m.Run in TestMain": {
			overrides: map[string]string{
				"lycaon/test/contract/sample/sample_test.go": "package sample\n\nimport (\n\t\"os\"\n\t\"testing\"\n\n\t\"github.com/lycaon/lycaon/test/contract/internal/hoststate\"\n)\n\nfunc TestMain(m *testing.M) {\n\trelease := hoststate.HoldAuthorityMemos()\n\tcode := m.Run()\n\trelease()\n\tos.Exit(code)\n}\n\nfunc TestUsesPackageHold(t *testing.T) {\n\t_ = hoststate.FreezeTurnAuthority()\n}\n",
			},
			want: invariantstest.Passes,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := invariants.Evaluate(invariantstest.Fixture(t, invariantstest.With(hoststateHoldTree, tc.overrides)), holdRule)
			invariantstest.Expect(t, res, tc.want, 1)
			if tc.wantPos != "" && !strings.HasPrefix(res.Violations[0], tc.wantPos) {
				t.Fatalf("violation = %q, want prefix %q", res.Violations[0], tc.wantPos)
			}
		})
	}
}
