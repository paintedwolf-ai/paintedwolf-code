package invariants_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
)

func TestCommandSettlementBoundaries(t *testing.T) {
	tests := []struct {
		rule, path, source, boundary, bypass string
		violations                           int
	}{
		{
			rule: "command-redirects-publish-through-settlement",
			path: "lycaon/internal/commandprogram/redirect.go",
			source: `package commandprogram
import "context"
type OutputPublished struct{}
type StagedOutput interface {
	Publish(context.Context) (OutputPublished, error)
	Abandon(context.Context) error
}
type stagedRedirect struct{}
func (stagedRedirect) finish(out StagedOutput) {
	out.Publish(context.Background())
}
type unrelated struct{}
func (unrelated) Publish() {}
func valid() { unrelated{}.Publish() }
`,
			boundary: "finish",
			bypass: `
func direct(out StagedOutput) { out.Publish(context.Background()) }
func detached(out interface{ Publish(context.Context) (OutputPublished, error) }) {
	_ = out.Publish
}
func expression() { _ = StagedOutput.Publish }
`,
			violations: 3,
		},
		{
			rule: "command-child-receipts-settle-through-dispatcher",
			path: "lycaon/internal/tools/native/command/dispatcher.go",
			source: `package command
import (
	"context"
	store "github.com/lycaon/lycaon/internal/invocation"
)
type ProgramDispatcher struct{}
func (ProgramDispatcher) settleWith(r *store.SQLRecorder) {
	r.SettleChild(context.Background(), "", store.Settlement{})
}
type unrelated struct{}
func (unrelated) SettleChild(string) {}
func valid() { unrelated{}.SettleChild("") }
`,
			boundary: "settleWith",
			bypass: `
func direct(r *store.SQLRecorder) { r.SettleChild(context.Background(), "", store.Settlement{}) }
func detached(r interface{ SettleChild(context.Context, string, store.Settlement) error }) {
	_ = r.SettleChild
}
func expression() { _ = (*store.SQLRecorder).SettleChild }
`,
			violations: 3,
		},
	}
	const recorder = `package invocation
import "context"
type SQLRecorder struct{}
type Settlement struct{}
func (*SQLRecorder) SettleChild(context.Context, string, Settlement) error { return nil }
`
	for _, tt := range tests {
		t.Run(tt.rule, func(t *testing.T) {
			t.Parallel()
			rule := invariantstest.Rule(t, forbiddencalls.Family, tt.rule)
			for _, control := range []struct {
				name, source string
				want         invariantstest.Outcome
				violations   int
			}{
				{"permitted boundary and unrelated method", tt.source, invariantstest.Passes, 0},
				{"direct and indirect bypasses", tt.source + tt.bypass, invariantstest.Violates, tt.violations},
				{"renamed boundary", strings.ReplaceAll(tt.source, tt.boundary, "renamed"), invariantstest.MissesAnchor, 0},
			} {
				t.Run(control.name, func(t *testing.T) {
					files := map[string]string{
						tt.path:                               control.source,
						"lycaon/internal/invocation/store.go": recorder,
					}
					invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, files), rule), control.want, control.violations)
				})
			}
		})
	}
}
