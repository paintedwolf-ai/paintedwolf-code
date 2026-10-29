package contract

import (
	"slices"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type laneStep struct {
	ID   string            `yaml:"id"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	If   string            `yaml:"if"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

type laneJob struct {
	Uses        string            `yaml:"uses"`
	Needs       yaml.Node         `yaml:"needs"`
	If          string            `yaml:"if"`
	RunsOn      yaml.Node         `yaml:"runs-on"`
	Environment string            `yaml:"environment"`
	Env         map[string]string `yaml:"env"`
	Outputs     map[string]string `yaml:"outputs"`
	Steps       []laneStep        `yaml:"steps"`
}

type laneWorkflow struct {
	On struct {
		WorkflowDispatch struct {
			Inputs map[string]yaml.Node `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]laneJob `yaml:"jobs"`
}

func parseLane(t *testing.T, path string) laneWorkflow {
	t.Helper()
	var workflow laneWorkflow
	raw := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), path)
	contractcheck.FailErr(t, "parse "+path, yaml.Unmarshal([]byte(raw), &workflow))
	return workflow
}

func (job laneJob) needs() []string {
	if job.Needs.Kind == yaml.ScalarNode {
		return []string{job.Needs.Value}
	}
	var names []string
	for _, node := range job.Needs.Content {
		names = append(names, node.Value)
	}
	return names
}

// text is everything the job hands its steps: environments, inputs and scripts.
func (job laneJob) text() string {
	var parts []string
	for _, value := range job.Env {
		parts = append(parts, value)
	}
	for _, step := range job.Steps {
		parts = append(parts, step.Uses, step.Run)
		for _, value := range step.Env {
			parts = append(parts, value)
		}
		for _, value := range step.With {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "\n")
}

func (job laneJob) artifacts(action string) []string {
	var names []string
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/"+action+"@") && step.With["name"] != "" {
			names = append(names, step.With["name"])
		}
	}
	return names
}

func (workflow laneWorkflow) ancestors(name string) map[string]bool {
	seen := map[string]bool{}
	var visit func(string)
	visit = func(job string) {
		for _, need := range workflow.Jobs[job].needs() {
			if !seen[need] {
				seen[need] = true
				visit(need)
			}
		}
	}
	visit(name)
	return seen
}

func sortedJobs(workflow laneWorkflow) []string {
	names := make([]string, 0, len(workflow.Jobs))
	for name := range workflow.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var storageCredentials = []string{"secrets.CLOUDFLARE_API_TOKEN", "secrets.R2_BUCKET"}

// Every storage write, for every platform, follows the same gated immutable deposit.
func TestReleaseStorageWritesFollowTheSharedPublicationGate(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release.yml")
	gate := workflow.Jobs["publish-artifacts"]
	if !strings.Contains(gate.If, "needs.classify.outputs.publish == 'true'") || !slices.Contains(gate.needs(), "ship-gates") {
		t.Fatal("immutable publication must follow ship-gates and the classified publication decision")
	}
	writers := 0
	for _, name := range sortedJobs(workflow) {
		job := workflow.Jobs[name]
		writes := false
		for _, credential := range storageCredentials {
			writes = writes || strings.Contains(job.text(), credential)
		}
		if name == "select-release-channels" {
			if !workflow.ancestors(name)["ship-gates"] || job.Environment != "release-publication" ||
				!strings.Contains(job.text(), "scripts.release_distribution_contract.channels select") ||
				strings.Contains(job.text(), "secrets.FEED_SIGNING_KEYS_JSON") || strings.Contains(job.text(), "immutable-put") {
				t.Fatal("early channel selection must only read retained controls after shared admission")
			}
			continue
		}
		if !writes || name == "publish-artifacts" {
			continue
		}
		writers++
		ancestors := workflow.ancestors(name)
		if !ancestors["publish-artifacts"] || !ancestors["ship-gates"] || job.Environment != "release-publication" {
			t.Errorf("%s writes release storage without the shared ship-gates/publish-artifacts gate", name)
		}
	}
	if writers == 0 || !strings.Contains(workflow.Jobs["publish-linux-repositories"].text(), "secrets.R2_BUCKET") {
		t.Fatal("Linux repositories must publish through the shared storage owner")
	}
	if !workflow.ancestors("notify-website")["publish-linux-repositories"] {
		t.Fatal("the release is announced only after every public platform's channels are active")
	}
}

// Linux package trust is signing custody, never publication custody, mirroring the updater key.
func TestLinuxPackageKeyStaysInSigningCustody(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release.yml")
	var holders []string
	for _, name := range sortedJobs(workflow) {
		job := workflow.Jobs[name]
		if !strings.Contains(job.text(), "secrets.LINUX_PACKAGE_SIGNING_KEY") {
			continue
		}
		holders = append(holders, name)
		if job.Environment != "release-signing" {
			t.Errorf("%s receives the Linux package key outside release-signing", name)
		}
		for _, credential := range append(storageCredentials, "secrets.HOMEBREW_TAP_TOKEN", "secrets.WWW_DISPATCH_TOKEN") {
			if strings.Contains(job.text(), credential) {
				t.Errorf("%s holds the Linux package key with publication credential %s", name, credential)
			}
		}
	}
	if !slices.Equal(holders, []string{"build-release", "linux-pacman-repositories", "linux-repositories"}) {
		t.Fatalf("Linux package key holders = %v", holders)
	}
	// A halt signs only fresh apt Release files over restored generations, in signing custody.
	halt := parseLane(t, ".github/workflows/release-halt.yml")
	holders = nil
	for _, name := range sortedJobs(halt) {
		if strings.Contains(halt.Jobs[name].text(), "LINUX_PACKAGE_SIGNING_KEY") {
			holders = append(holders, name)
		}
	}
	signer := halt.Jobs["sign-native-restore"]
	if !slices.Equal(holders, []string{"sign-native-restore"}) || signer.Environment != "release-signing" {
		t.Fatalf("release halt package key holders = %v", holders)
	}
	for _, credential := range append(storageCredentials, "secrets.HOMEBREW_TAP_TOKEN", "secrets.WWW_DISPATCH_TOKEN") {
		if strings.Contains(signer.text(), credential) {
			t.Errorf("the halt signer holds the Linux package key with publication credential %s", credential)
		}
	}
	for _, step := range signer.Steps {
		if step.Env["LINUX_PACKAGE_SIGNING_KEY"] != "" && !strings.Contains(step.Run, "scripts.linux_release.repository_withdrawal resign") {
			t.Fatal("the halt signer may only re-sign prepared apt restores")
		}
	}
}

// Every consumed artifact is produced in this run by a job the consumer waits for, so
// publication reads only bytes this run built, signed and digested.
func TestReleaseArtifactsFlowOnlyAlongJobDependencies(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release.yml")
	producers := map[string][]string{}
	for _, name := range sortedJobs(workflow) {
		job := workflow.Jobs[name]
		outputs := job.artifacts("upload-artifact")
		if strings.HasPrefix(job.Uses, "./.github/workflows/") {
			called := parseLane(t, strings.TrimPrefix(job.Uses, "./"))
			for _, child := range sortedJobs(called) {
				outputs = append(outputs, called.Jobs[child].artifacts("upload-artifact")...)
			}
		}
		for _, artifact := range outputs {
			producers[artifact] = append(producers[artifact], name)
		}
	}
	if !slices.Equal(producers["release-public-artifacts"], []string{"aggregate-release"}) {
		t.Fatalf("release-public-artifacts producers = %v", producers["release-public-artifacts"])
	}
	for _, name := range sortedJobs(workflow) {
		job := workflow.Jobs[name]
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/download-artifact@") {
				continue
			}
			if step.With["run-id"] != "" {
				continue // checked by TestReleaseWorkflowsReadOnlyTheirOwnRunEvidence
			}
			artifact := step.With["name"]
			if pattern := step.With["pattern"]; artifact == "" && pattern != "" {
				artifact = strings.TrimSuffix(pattern, "*")
			}
			found := false
			if _, output, ok := strings.Cut(artifact, "${{ needs."); ok {
				// A name computed by a needed job's output must come from that job's own upload.
				producer, _, _ := strings.Cut(output, ".outputs.")
				found = workflow.ancestors(name)[producer] && len(workflow.Jobs[producer].artifacts("upload-artifact")) > 0
			}
			for produced, jobs := range producers {
				if produced == artifact || (step.With["pattern"] != "" && strings.HasPrefix(produced, artifact)) {
					for _, producer := range jobs {
						found = found || workflow.ancestors(name)[producer]
					}
				}
			}
			if !found {
				t.Errorf("%s downloads %q without waiting for the job that produces it", name, artifact)
			}
		}
	}
	for _, name := range []string{"linux-repositories", "linux-pacman-repositories", "publish-linux-repositories"} {
		if !slices.Contains(workflow.Jobs[name].artifacts("download-artifact"), "release-public-artifacts") {
			t.Fatalf("%s must read the same collected public artifacts as macOS publication", name)
		}
	}
}

// Admission is the shared exact-source qualification; no release job reads evidence
// from another run, a local campaign or an operator-supplied artifact.
func TestReleaseWorkflowsReadOnlyTheirOwnRunEvidence(t *testing.T) {
	t.Parallel()
	release := parseLane(t, ".github/workflows/release.yml")
	var inputs []string
	for name := range release.On.WorkflowDispatch.Inputs {
		inputs = append(inputs, name)
	}
	sort.Strings(inputs)
	if !slices.Equal(inputs, []string{"rehearsal_case", "rehearsal_run_id", "release_tag"}) {
		t.Fatalf("release dispatch inputs = %v; publication takes no external evidence", inputs)
	}
	for _, path := range []string{".github/workflows/release.yml", ".github/workflows/release-halt.yml"} {
		workflow := parseLane(t, path)
		for _, name := range sortedJobs(workflow) {
			job := workflow.Jobs[name]
			if strings.Contains(job.RunsOn.Value, "self-hosted") || slices.ContainsFunc(job.RunsOn.Content, func(node *yaml.Node) bool {
				return node.Value == "self-hosted"
			}) {
				t.Errorf("%s %s runs on a self-hosted runner", path, name)
			}
			text := job.text()
			for _, forbidden := range []string{"gh run download", "campaign", "installed_results", "test:linux:installed"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s %s reads external evidence (%q)", path, name, forbidden)
				}
			}
			for _, step := range job.Steps {
				if step.With["run-id"] != "" && name != "replay-bundle-upgrade" {
					t.Errorf("%s %s downloads another run's artifacts", path, name)
				}
			}
		}
	}
	replay := release.Jobs["replay-bundle-upgrade"]
	if replay.Environment != "" || strings.Contains(replay.text(), "secrets.") {
		t.Fatal("the diagnostic replay of retained fixtures must hold no release credential")
	}
	sign := strings.Contains(release.Jobs["build-release"].text(), "scripts.linux_release.release_signing")
	if !sign || !strings.Contains(release.Jobs["classify"].text(), "scripts/ci_verification.py verified") {
		t.Fatal("Linux packages are signed in the shared build and admitted by the shared qualification")
	}
}

// A halt withdraws Linux repository offers in the same protected run as the feeds.
func TestReleaseHaltWithdrawsLinuxRepositories(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release-halt.yml")
	halt, apply := workflow.Jobs["halt"], workflow.Jobs["apply-halt"]
	var prepared, applied bool
	for _, step := range halt.Steps {
		prepared = prepared || strings.Contains(step.Run, "scripts.linux_release.repository_withdrawal prepare")
	}
	for _, step := range apply.Steps {
		applied = applied || strings.Contains(step.Run, "scripts.linux_release.repository_withdrawal apply") && strings.Contains(step.Run, "--signed")
	}
	if !prepared || !applied || !strings.Contains(apply.If, "!inputs.dry_run") ||
		halt.Environment != "release-publication" || apply.Environment != "release-publication" {
		t.Fatal("release halt must prepare Linux withdrawal in dry runs and apply it only when publishing")
	}
}

// The cache key reaches only its protected publisher after the shared release gates.
func TestNixCacheCredentialReachesOnlyItsGatedPublisher(t *testing.T) {
	t.Parallel()
	release := parseLane(t, ".github/workflows/release.yml")
	caller := release.Jobs["linux-delivery"]
	ancestors := release.ancestors("linux-delivery")
	for _, gate := range []string{"ship-gates", "publish-artifacts", "publish-linux-repositories"} {
		if !ancestors[gate] {
			t.Errorf("Linux delivery runs before %s", gate)
		}
	}
	for _, condition := range []string{"needs.classify.outputs.publish == 'true'", "needs.classify.outputs.linux_public == 'true'",
		"needs.publish-linux-repositories.result == 'success'"} {
		if !strings.Contains(caller.If, condition) {
			t.Errorf("Linux delivery must require %s", condition)
		}
	}
	const credential = "secrets.NIX_CACHE_SIGNING_KEY"
	for _, name := range sortedJobs(release) {
		if strings.Contains(release.Jobs[name].text(), credential) {
			t.Errorf("release.yml %s holds the cache key outside the delivery workflow", name)
		}
	}
	delivery := parseLane(t, ".github/workflows/release-linux-delivery.yml")
	var holders []string
	for _, name := range sortedJobs(delivery) {
		if strings.Contains(delivery.Jobs[name].text(), credential) {
			holders = append(holders, name)
		}
	}
	if !slices.Equal(holders, []string{"publish-nix-cache"}) {
		t.Fatalf("cache key holders = %v, want only publish-nix-cache", holders)
	}
	job := delivery.Jobs["publish-nix-cache"]
	var rechecks bool
	for _, step := range job.Steps {
		rechecks = rechecks || (strings.Contains(step.Run, "scripts.nix_cache.release_lane publish") && step.Env["GH_TOKEN"] == "${{ github.token }}")
	}
	if job.Environment != "release-publication" || !rechecks {
		t.Error("the cache publisher must reread Qualification in release-publication")
	}
	for _, name := range sortedJobs(delivery) {
		job := delivery.Jobs[name]
		for _, credential := range storageCredentials {
			if strings.Contains(job.text(), credential) && job.Environment != "release-publication" {
				t.Errorf("%s writes release storage outside release-publication", name)
			}
		}
	}
	checks := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/release-secrets-check.yml")
	if !strings.Contains(checks, credential) {
		t.Error("release secrets check must validate the cache key")
	}
}

// A halt withdraws Nix cache availability in the same protected run as the repositories.
func TestReleaseHaltWithdrawsNixCacheAvailability(t *testing.T) {
	t.Parallel()
	apply := parseLane(t, ".github/workflows/release-halt.yml").Jobs["apply-halt"]
	for _, step := range apply.Steps {
		if strings.Contains(step.Run, "scripts.nix_cache.release_lane withdraw") {
			if !strings.Contains(apply.If, "!inputs.dry_run") {
				t.Fatal("Nix cache withdrawal must apply only outside dry runs")
			}
			return
		}
	}
	t.Fatal("release halt must withdraw the halted release's Nix cache availability")
}

func TestReleaseHaltRequiresCompleteSymmetricPreparationBeforeEveryMutation(t *testing.T) {
	t.Parallel()
	workflow := parseLane(t, ".github/workflows/release-halt.yml")
	halt := workflow.Jobs["halt"]
	steps := map[string]laneStep{}
	positions := map[string]int{}
	for position, step := range halt.Steps {
		if step.ID != "" {
			steps[step.ID], positions[step.ID] = step, position
		}
	}
	selection := steps["halt_selection"]
	if !strings.Contains(selection.Run, "--select-only") || !strings.Contains(selection.Run, "--distribution-output") ||
		strings.Contains(selection.Run, "--prepare-output") || halt.Env["FEED_SIGNING_KEYS_JSON"] != "" {
		t.Fatal("typed halt selection must precede effect-specific signing and preparation")
	}
	barrier := steps["halt_barrier"]
	for _, owner := range []string{"linux_plan", "ledger_plan", "feed_plan", "nix_plan", "distribution_plan"} {
		step := steps[owner]
		if positions[owner] <= positions["halt_selection"] || positions[owner] >= positions["halt_barrier"] ||
			!strings.Contains(step.If, "!cancelled()") || !strings.Contains(barrier.Env["HALT_PREPARATION_OUTCOMES"], "steps."+owner+".outcome") {
			t.Fatalf("%s must prepare independently and participate in the complete barrier", owner)
		}
	}
	if !strings.Contains(barrier.Run, "release_distribution_contract.halt_effects") {
		t.Fatal("the barrier must retain every owner's actions and refusals")
	}
	mutation := func(step laneStep) bool {
		return strings.Contains(step.Run, "--apply-prepared") || strings.Contains(step.Run, "repository_withdrawal apply") ||
			strings.Contains(step.Run, "nix_cache.release_lane withdraw") || strings.Contains(step.Run, "release_withdrawal.py apply")
	}
	if slices.ContainsFunc(halt.Steps, mutation) || halt.Outputs["barrier"] != "${{ steps.halt_barrier.outcome }}" {
		t.Fatal("the preparing job applies nothing and hands its barrier outcome to the later jobs")
	}
	// Re-signing apt restores is preparation: it follows the barrier and precedes every effect.
	signer, apply := workflow.Jobs["sign-native-restore"], workflow.Jobs["apply-halt"]
	for _, condition := range []string{"!cancelled()", "!inputs.dry_run", "needs.halt.outputs.barrier == 'success'"} {
		if !strings.Contains(signer.If, condition) || !strings.Contains(apply.If, condition) {
			t.Fatalf("any preparation refusal must block re-signing and every halt mutation (%s)", condition)
		}
	}
	if !strings.Contains(apply.If, "needs.sign-native-restore.result == 'success'") || !workflow.ancestors("apply-halt")["sign-native-restore"] {
		t.Fatal("halt effects apply only after apt restores are signed")
	}
	mutations := 0
	for _, step := range apply.Steps {
		if mutation(step) {
			mutations++
		}
	}
	if mutations != 4 {
		t.Fatalf("halt mutation owners = %d, want feed/ledger, native, Nix and package/website", mutations)
	}
}
