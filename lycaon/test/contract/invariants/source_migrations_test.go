package invariants_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/enumclosure"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
)

func TestRenderImportRule(t *testing.T) {
	t.Parallel()
	check := rule(t, forbiddencalls.Family, "render-path-files-avoid-io-imports")
	const helper = "lycaon/internal/guidance/helper_linux.go"
	files := map[string]string{
		"lycaon/internal/guidance/renderer.go": `package guidance
type StaticRejectFormatter struct{}
func (f StaticRejectFormatter) Format() { f.helper() }
func (f StaticRejectFormatter) helper() { local[int](0) }
func EnvelopeHintMessage() {}
`,
		helper: "package guidance; func local[T any](T) {}",
		"lycaon/internal/guidance/subpackage/helper.go": "package subpackage; import _ \"net\"; func local() {}",
		"lycaon/internal/parse/validate.go":             "package parse; type DefaultService struct{}; func (DefaultService) Validate() {}",
		"lycaon/internal/hostcmd/validate.go":           "package hostcmd; type Runner struct{}; func (Runner) ValidateStages() {}",
		"lycaon/internal/scan/output/parse.go":          "package output; func ParseOutput() {}",
		"lycaon/internal/session/guidancedelivery/emit.go": `package guidancedelivery
type Service struct{}
func (Service) Emit() {}
func (Service) EmitEager() {}
`,
	}
	expect(t, invariants.Evaluate(fixture(t, files), check), passes, 0)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		helper: `package guidance; import _ "net/http"; func local[T any](T) {}`,
	})), check), violates, 1)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		"lycaon/internal/scan/output/parse.go": "package output; func Renamed() {}",
	})), check), missesAnchor, 0)
}

func TestDiscoveryEnumFindsInferredConstants(t *testing.T) {
	t.Parallel()
	check := rule(t, enumclosure.Family, "provider-discovery-strategies-have-rationale")
	rel := "lycaon/internal/llm/providerprofile/profile.go"
	files := map[string]string{
		rel: `package providerprofile
type DiscoveryStrategy string
const Configured DiscoveryStrategy = "configured"
const Undocumented = DiscoveryStrategy("future")
const Duplicate = Configured
`,
	}
	result := invariants.Evaluate(fixture(t, files), check)
	expect(t, result, violates, 0)
	for _, want := range []string{`missing future`, `duplicates Configured with value "configured"`, `unexpected none`} {
		if !strings.Contains(result.String(), want) {
			t.Errorf("missing evidence for %q in %s", want, result)
		}
	}
}

func TestSourceFeedDoorRule(t *testing.T) {
	t.Parallel()
	check := rule(t, enumclosure.Family, "source-feed-doors-have-emit-sites")
	const (
		sourcefeed = "lycaon/internal/sourcefeed/doors.go"
		editor     = "lycaon/internal/editordoc/commit.go"
		importFeed = "\nimport feed \"github.com/lycaon/lycaon/internal/sourcefeed\"\n"
	)
	files := map[string]string{
		sourcefeed: `package sourcefeed
type EmitDoor string
const (
	Agent EmitDoor = "agent.mutation"
	Editor EmitDoor = "editor.save"
	Promotion EmitDoor = "worker.promotion"
	Put EmitDoor = "user.put"
	Post EmitDoor = "user.post"
	Rename EmitDoor = "lifecycle.rename"
	Delete EmitDoor = "lifecycle.delete"
	Copy EmitDoor = "lifecycle.copy"
	Replace EmitDoor = "search.replace_apply"
)
func Emit() {}
func EmitTx() {}
func EmitBatch() {}
func EmitBatchTx() {}
`,
		editor: "package editordoc" + importFeed + "type saveCommitter struct{}; func (saveCommitter) commit() { feed.EmitTx() }",
		"lycaon/internal/projectsource/commit.go": "package projectsource" + importFeed + `
type sourceSettlement struct{}
func commitSourceMutationTxWithCache() { feed.EmitBatchTx() }
func (s *sourceSettlement) commitWithoutStore() { feed.EmitBatch() }
`,
		"lycaon/internal/tools/native/record.go": "package native" + importFeed + `
func commitAgentMutationTx() { feed.EmitTx() }
func commitAgentMutationWithoutStore() { feed.Emit() }
`,
		"lycaon/internal/worker/promotion.go": "package worker" + importFeed + "type SQLStore struct{}; func (*SQLStore) CommitPromotion() { feed.EmitBatchTx() }",
	}
	expect(t, invariants.Evaluate(fixture(t, files), check), passes, 0)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		editor: strings.Replace(files[editor], "feed.EmitTx()", "_ = feed.EmitTx", 1),
	})), check), violates, 1)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		editor: strings.Replace(files[editor], "feed.EmitTx()", "_ = feed.EmitTx; feed := struct{ EmitTx func() }{}; feed.EmitTx()", 1),
	})), check), violates, 1)
	expect(t, invariants.Evaluate(fixture(t, with(files, map[string]string{
		sourcefeed: files[sourcefeed] + "\nconst Unregistered = EmitDoor(\"unnamed\")\n",
	})), check), violates, 2)
}
