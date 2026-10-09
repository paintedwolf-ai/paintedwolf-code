package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// The reusable verification matrix takes its profile's cap from the catalog through its plan job.
const catalogMaxParallel = "${{ fromJSON(needs.plan.outputs.max-parallel) }}"

// Each run of a per-event workflow handles one issue or completed run in one short job.
const perEventMinutes = 15

var inputReference = regexp.MustCompile(`^\$\{\{ inputs\.([a-z-]+) \}\}$`)

// eventCondition is a job condition on the triggering event alone, such as issue-sweep's.
var eventCondition = regexp.MustCompile(`^(?:\$\{\{ )?github\.event_name (==|!=) '([a-z_]+)'(?: \}\})?$`)

type hostedCapacity struct {
	Runners      int            `json:"runners"`
	MacOSRunners int            `json:"macos_runners"`
	MaxParallel  map[string]int `json:"max_parallel"`
	Footprints   map[string]int `json:"footprints"`
}

type capacityLane struct {
	Profiles []string `json:"profiles"`
	Runner   string   `json:"runner"`
	Shards   int      `json:"shards"`
}

type capacityJob struct {
	Uses        string
	If          string
	With        map[string]string
	Needs       yaml.Node
	RunsOn      yaml.Node `yaml:"runs-on"`
	Timeout     string    `yaml:"timeout-minutes"`
	Concurrency yaml.Node
	Strategy    struct {
		MaxParallel string `yaml:"max-parallel"`
		Matrix      yaml.Node
	}
}

type capacityWorkflow struct {
	On          map[string]yaml.Node
	Concurrency yaml.Node
	Jobs        map[string]capacityJob
}

// footprint counts the hosted runners a workflow run can hold at once.
type footprint struct{ total, macos int }

type capacityPlan struct {
	root      string
	capacity  hostedCapacity
	lanes     map[string]capacityLane
	workflows map[string]capacityWorkflow
}

func loadCapacityPlan(t *testing.T) capacityPlan {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	var catalog struct {
		Capacity hostedCapacity          `json:"capacity"`
		CI       map[string]capacityLane `json:"ci"`
	}
	data := contractcheck.ReadRepoFile(t, root, "scripts/verification-plan.json")
	contractcheck.FailErr(t, "decode verification plan", json.Unmarshal([]byte(data), &catalog))
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	contractcheck.FailErr(t, "read workflows", err)
	plan := capacityPlan{root: root, capacity: catalog.Capacity, lanes: catalog.CI, workflows: map[string]capacityWorkflow{}}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		var workflow capacityWorkflow
		data := contractcheck.ReadRepoFile(t, root, filepath.Join(".github", "workflows", entry.Name()))
		contractcheck.FailErr(t, "decode "+entry.Name(), yaml.Unmarshal([]byte(data), &workflow))
		plan.workflows[entry.Name()] = workflow
	}
	return plan
}

// triggered lists the workflows that start runs on their own events, other than the CI gate.
func (p capacityPlan) triggered() []string {
	var names []string
	for name, workflow := range p.workflows {
		_, reusable := workflow.On["workflow_call"]
		if name != "ci.yml" && !(reusable && len(workflow.On) == 1) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// group is the concurrency group that admits one run at a time: the workflow's, or its only job's.
func (p capacityPlan) group(t *testing.T, name string) string {
	t.Helper()
	workflow := p.workflows[name]
	node := workflow.Concurrency
	if node.Kind == 0 && len(workflow.Jobs) == 1 {
		for _, job := range workflow.Jobs {
			node = job.Concurrency
		}
	}
	var settings struct {
		Group  string
		Cancel string `yaml:"cancel-in-progress"`
	}
	if node.Kind == yaml.ScalarNode {
		settings.Group = node.Value
	} else if node.Kind == yaml.MappingNode {
		contractcheck.FailErr(t, "decode concurrency of "+name, node.Decode(&settings))
	}
	if settings.Group == "" {
		t.Fatalf("%s must run one at a time in a concurrency group", name)
	}
	if settings.Cancel != "" && settings.Cancel != "false" {
		t.Fatalf("%s must finish a started run; a newer run replaces only a pending one, got cancel-in-progress %q", name, settings.Cancel)
	}
	return settings.Group
}

// runFootprint is the widest footprint of a run across the workflow's triggering events.
func (p capacityPlan) runFootprint(t *testing.T, name string) footprint {
	t.Helper()
	var widest footprint
	for event := range p.workflows[name].On {
		got := p.footprint(t, name, event, nil)
		widest = footprint{total: max(widest.total, got.total), macos: max(widest.macos, got.macos)}
	}
	return widest
}

func (p capacityPlan) footprint(t *testing.T, name, event string, inputs map[string]string) footprint {
	t.Helper()
	workflow, ok := p.workflows[name]
	if !ok {
		t.Fatalf("missing workflow %s", name)
	}
	weights := map[string]footprint{}
	ancestors := map[string]map[string]bool{}
	for job, spec := range workflow.Jobs {
		if admits(spec.If, event) {
			weights[job] = p.jobFootprint(t, name, job, event, spec, inputs)
		}
	}
	var visit func(string) map[string]bool
	visit = func(job string) map[string]bool {
		if seen, ok := ancestors[job]; ok {
			return seen
		}
		seen := map[string]bool{}
		ancestors[job] = seen
		for _, need := range capacityNeeds(t, workflow.Jobs[job].Needs) {
			seen[need] = true
			for ancestor := range visit(need) {
				seen[ancestor] = true
			}
		}
		return seen
	}
	for job := range workflow.Jobs {
		visit(job)
	}
	return footprint{
		total: widestAntichain(t, name, ancestors, func(job string) int { return weights[job].total }),
		macos: widestAntichain(t, name, ancestors, func(job string) int { return weights[job].macos }),
	}
}

// admits reports whether a job runs for the event: only a condition on the event alone can exclude it.
func admits(condition, event string) bool {
	match := eventCondition.FindStringSubmatch(condition)
	if len(match) != 3 {
		return true
	}
	return (match[2] == event) == (match[1] == "==")
}

// widestAntichain is the heaviest set of jobs that can run at once: none needs another, directly or transitively.
func widestAntichain(t *testing.T, name string, ancestors map[string]map[string]bool, weight func(string) int) int {
	t.Helper()
	var jobs []string
	for job := range ancestors {
		jobs = append(jobs, job)
	}
	slices.Sort(jobs)
	if len(jobs) > 20 {
		t.Fatalf("%s has %d jobs; split it before its footprint can be bounded", name, len(jobs))
	}
	widest := 0
	for subset := 1; subset < 1<<len(jobs); subset++ {
		sum, independent := 0, true
		for i, job := range jobs {
			if subset&(1<<i) == 0 {
				continue
			}
			sum += weight(job)
			for j, other := range jobs {
				if subset&(1<<j) != 0 && ancestors[job][other] {
					independent = false
				}
			}
		}
		if independent && sum > widest {
			widest = sum
		}
	}
	return widest
}

func (p capacityPlan) jobFootprint(t *testing.T, workflow, name, event string, job capacityJob, inputs map[string]string) footprint {
	t.Helper()
	if callee, ok := strings.CutPrefix(job.Uses, "./.github/workflows/"); ok {
		with := map[string]string{}
		for key, value := range job.With {
			if match := inputReference.FindStringSubmatch(value); match != nil {
				value = inputs[match[1]]
			}
			with[key] = value
		}
		return p.footprint(t, callee, event, with)
	}
	runner := job.RunsOn.Value
	if job.Strategy.Matrix.Kind == 0 {
		return footprint{total: 1, macos: boolCount(strings.HasPrefix(runner, "macos"))}
	}
	entries, macEntries := matrixEntries(job.Strategy.Matrix)
	limit := p.maxParallel(t, workflow+"/"+name, job.Strategy.MaxParallel, inputs)
	if entries < 0 && limit < 0 {
		t.Fatalf("%s/%s plans its matrix at run time and must cap max-parallel", workflow, name)
	}
	total := cappedCount(entries, limit)
	switch {
	case strings.HasPrefix(runner, "macos"):
		return footprint{total: total, macos: total}
	case runner != "${{ matrix.runner }}":
		return footprint{total: total}
	case job.Strategy.MaxParallel == catalogMaxParallel:
		return footprint{total: total, macos: cappedCount(p.macosLanes(inputs["profile"]), limit)}
	case entries < 0:
		// A runner planned at run time may be macOS.
		return footprint{total: total, macos: total}
	}
	return footprint{total: total, macos: cappedCount(macEntries, limit)}
}

// maxParallel resolves a literal cap, a caller's input, or the catalog cap of the caller's profile; -1 is no cap.
func (p capacityPlan) maxParallel(t *testing.T, job, value string, inputs map[string]string) int {
	t.Helper()
	if value == "" {
		return -1
	}
	if value == catalogMaxParallel {
		limit, ok := p.capacity.MaxParallel[inputs["profile"]]
		if !ok {
			t.Fatalf("%s runs profile %q, which declares no catalog cap", job, inputs["profile"])
		}
		return limit
	}
	if match := inputReference.FindStringSubmatch(value); match != nil {
		value = inputs[match[1]]
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 {
		t.Fatalf("%s max-parallel %q must resolve to a positive number in every caller", job, value)
	}
	return limit
}

func (p capacityPlan) macosLanes(profile string) int {
	count := 0
	for _, lane := range p.lanes {
		if slices.Contains(lane.Profiles, profile) && strings.HasPrefix(lane.Runner, "macos") {
			count += max(lane.Shards, 1)
		}
	}
	return count
}

// matrixEntries counts a static matrix's jobs and its macOS runner entries; -1 when planned at run time.
func matrixEntries(matrix yaml.Node) (int, int) {
	if matrix.Kind != yaml.MappingNode {
		return -1, -1
	}
	entries, macos := 1, 1
	for i := 0; i+1 < len(matrix.Content); i += 2 {
		key, values := matrix.Content[i].Value, matrix.Content[i+1]
		if key == "include" || key == "exclude" || values.Kind != yaml.SequenceNode {
			return -1, -1
		}
		entries *= len(values.Content)
		if key != "runner" {
			macos *= len(values.Content)
			continue
		}
		matching := 0
		for _, value := range values.Content {
			matching += boolCount(strings.HasPrefix(value.Value, "macos"))
		}
		macos *= matching
	}
	return entries, macos
}

func cappedCount(entries, limit int) int {
	if entries < 0 {
		return limit
	}
	if limit < 0 {
		return entries
	}
	return min(entries, limit)
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func capacityNeeds(t *testing.T, node yaml.Node) []string {
	t.Helper()
	if node.Kind == 0 {
		return nil
	}
	if node.Kind == yaml.ScalarNode {
		return []string{node.Value}
	}
	var needs []string
	contractcheck.FailErr(t, "decode job dependencies", node.Decode(&needs))
	return needs
}

func readQueueSettings(t *testing.T, root string) map[string]any {
	t.Helper()
	var settings map[string]any
	data := contractcheck.ReadRepoFile(t, root, "scripts/ci_policy/queue-settings.json")
	contractcheck.FailErr(t, "decode queue settings", json.Unmarshal([]byte(data), &settings))
	return settings
}

// Every workflow other than the gate holds a declared, bounded number of runners:
// one run at a time, each matrix capped. Per-event runs are one short job each.
func TestTriggeredWorkflowsHoldBoundedRunners(t *testing.T) {
	t.Parallel()
	plan := loadCapacityPlan(t)
	triggered := plan.triggered()
	for name := range plan.capacity.Footprints {
		if !slices.Contains(triggered, name) {
			t.Errorf("capacity declares %s, which is not a triggered workflow", name)
		}
	}
	for _, name := range triggered {
		declared, ok := plan.capacity.Footprints[name]
		if !ok {
			t.Errorf("%s must declare its runner footprint in the catalog's capacity table", name)
			continue
		}
		group := plan.group(t, name)
		if got := plan.runFootprint(t, name); got.total > declared {
			t.Errorf("%s can hold %d runners at once, more than its declared %d", name, got.total, declared)
		}
		if !strings.Contains(group, "${{") {
			continue
		}
		if declared != 1 {
			t.Errorf("%s runs once per event, so each run must be a single job, not %d", name, declared)
		}
		for job, spec := range plan.workflows[name].Jobs {
			if minutes, err := strconv.Atoi(spec.Timeout); err != nil || minutes > perEventMinutes {
				t.Errorf("%s/%s runs once per event and must finish within %d minutes, got %q", name, job, perEventMinutes, spec.Timeout)
			}
		}
	}
}

// Every bounded class at its widest, together with the merge queue's groups at their cap, fits the plan's runners.
func TestBoundedClassesFitTheHostedRunners(t *testing.T) {
	t.Parallel()
	plan := loadCapacityPlan(t)
	runners, macos := map[string]int{}, map[string]int{}
	for _, name := range plan.triggered() {
		group := plan.group(t, name)
		if strings.Contains(group, "${{") {
			continue
		}
		runners[group] = max(runners[group], plan.capacity.Footprints[name])
		macos[group] = max(macos[group], plan.runFootprint(t, name).macos)
	}
	settings := readQueueSettings(t, plan.root)
	groups, _ := settings["max_entries_to_build"].(float64)
	queue := int(groups) * plan.capacity.MaxParallel["integration"]
	total, macTotal := queue, 0
	for group, count := range runners {
		total += count
		macTotal += macos[group]
	}
	if total > plan.capacity.Runners {
		t.Errorf("bounded classes %v and %d merge-queue jobs need %d runners; the plan runs %d", runners, queue, total, plan.capacity.Runners)
	}
	if macTotal > plan.capacity.MacOSRunners {
		t.Errorf("bounded classes %v need %d macOS runners; the plan runs %d", macos, macTotal, plan.capacity.MacOSRunners)
	}
}

// The gate's verification matrix takes its cap from the catalog, so each profile's footprint has one source.
func TestVerificationMatrixTakesItsCatalogCap(t *testing.T) {
	t.Parallel()
	plan := loadCapacityPlan(t)
	verify := plan.workflows["verification.yml"].Jobs["verify"]
	if verify.Strategy.MaxParallel != catalogMaxParallel {
		t.Fatalf("verification must cap its matrix with the catalog's profile cap, got %q", verify.Strategy.MaxParallel)
	}
	for _, name := range []string{"qualification.yml", "nightly.yml"} {
		for job, spec := range plan.workflows[name].Jobs {
			if spec.Uses == "./.github/workflows/verification.yml" {
				if _, ok := plan.capacity.MaxParallel[spec.With["profile"]]; !ok {
					t.Errorf("%s/%s must run a catalog profile with a declared cap, got %q", name, job, spec.With["profile"])
				}
			}
		}
	}
}
