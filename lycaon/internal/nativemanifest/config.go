// Package nativemanifest loads the native tool catalog.
package nativemanifest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
)

const (
	TierReversible             = "reversible"
	TierRecoverable            = "recoverable"
	TierIrreversible           = "irreversible"
	BatchSerial                = "serial"
	BatchShared                = "shared"
	BatchSameTool              = "same_tool"
	TurnOrderNormal            = "normal"
	TurnOrderLate              = "late"
	TurnOrderTerminal          = "terminal"
	LifecycleReadOnly          = "read_only"
	LifecycleDBTransaction     = "db_transaction"
	LifecycleJournaledMutation = "journaled_mutation"
	LifecycleDurableJob        = "durable_job"
	LifecycleEffectAttempt     = "effect_attempt"
	LifecycleEphemeralControl  = "ephemeral_control"
)

// Outbound screens a catalog tool may name in secret_reference_surface.
const (
	SecretSurfaceCommand     = "command"
	SecretSurfaceTerminal    = "terminal"
	SecretSurfaceHTTPRequest = "http_request"
	SecretSurfaceFile        = "file"
)

// Allowed grant_target_scope values on native-tools.yaml. Omitted means ScopeTool.
const (
	ScopeCommand = "command"
	ScopePath    = "path"
	ScopeTool    = "tool"

	// Live resource facts that add control tools to a compiled surface.
	ResourceFactCommandJobs = "command_jobs"
	ResourceFactTerminals   = "terminals"
	ResourceFactPages       = "pages"
	ResourceFactHeldCalls   = "held_calls"
)

// Config holds native tools and their invocation contracts.
type Config struct {
	Native                map[string][]string `yaml:"native"`
	ApprovalReversibility map[string]string   `yaml:"approval_reversibility"`
	Owner                 map[string]string   `yaml:"owner"`
	BatchPolicy           map[string]string   `yaml:"batch_policy"`
	// BatchOptInArg names the argument that enables sibling concurrency.
	BatchOptInArg         map[string]string   `yaml:"batch_opt_in_arg"`
	BatchConcurrencyLimit map[string]int      `yaml:"batch_concurrency_limit"`
	BatchSerialWhenArg    map[string]string   `yaml:"batch_serial_when_arg"`
	TurnOrder             map[string]string   `yaml:"turn_order"`
	Lifecycle             map[string]string   `yaml:"lifecycle"`
	GrantTargetScope      map[string]string   `yaml:"grant_target_scope"`
	RequiresWorkerBranch  []string            `yaml:"requires_worker_branch"`
	SpawnsProcess         []string            `yaml:"spawns_process"`
	ExecutionCapabilities map[string][]string `yaml:"execution_capabilities"`
	Families              map[string][]string `yaml:"families"`
	// Companions load with a tool a request selects.
	Companions      map[string][]string `yaml:"companions"`
	ResourceImplied map[string][]string `yaml:"resource_implied"`
	// GrantIdentityNeutralArgs names arguments a grant identity ignores.
	GrantIdentityNeutralArgs map[string][]string `yaml:"grant_identity_neutral_args"`
	// ChunkablePayload names tools whose arguments can be split across calls.
	ChunkablePayload []string `yaml:"chunkable_payload"`
	// SurveyNeutral names bookkeeping tools that neither observe nor act.
	SurveyNeutral []string `yaml:"survey_neutral"`
	// DetachAfterBudget names read-only tools whose call may outlive its foreground wait.
	DetachAfterBudget []string `yaml:"detach_after_budget"`
	// MutatesPath names tools that write a path their own arguments declare.
	MutatesPath []string `yaml:"mutates_path"`
	// MutatesContent names tools whose arguments carry authored file content.
	MutatesContent []string `yaml:"mutates_content"`
	// MultiRoot maps path-handling capabilities to tool ids.
	MultiRoot map[string][]string `yaml:"multi_root"`
	// SessionScope names the session shape each listed tool belongs to.
	SessionScope map[string]string `yaml:"session_scope"`
	// SocketArg names the argument whose AF_UNIX socket the tool dials itself.
	SocketArg map[string]string `yaml:"socket_arg"`
	// SecretReferenceSurface names the outbound screen of each tool that resolves managed-secret references.
	SecretReferenceSurface map[string]string `yaml:"secret_reference_surface"`
	// SecretReferenceArgs names the value slots resolved when SecretReferenceSurface is SecretSurfaceFile.
	SecretReferenceArgs map[string][]string `yaml:"secret_reference_args"`
	// BoundedInProcess names tools whose execution runs synchronously in-process.
	BoundedInProcess []string `yaml:"bounded_in_process"`
	// HostProducedTools lists rule-plane tools without model schemas.
	HostProducedTools []string `yaml:"host_produced_tools"`
	// RuntimeRegisteredPrefixes identify tools registered after boot.
	RuntimeRegisteredPrefixes []string `yaml:"runtime_registered_prefixes"`
}

// Load reads the native tool manifest.
func Load() (Config, error) {
	data, err := config.Read(config.NativeTools)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// AllTools returns flattened native tool names.
func (c Config) AllTools() []string {
	var out []string
	for _, group := range c.Native {
		out = append(out, group...)
	}
	return out
}

// HasTool reports whether name is listed in the manifest.
func (c Config) HasTool(name string) bool {
	for _, group := range c.Native {
		for _, tool := range group {
			if tool == name {
				return true
			}
		}
	}
	return false
}

// NativeToolNames returns every tool the native.* groups declare, sorted.
func (c Config) NativeToolNames() []string {
	var out []string
	for _, group := range c.Native {
		for _, tool := range group {
			if tool = strings.TrimSpace(tool); tool != "" {
				out = append(out, tool)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Group returns tools in a native.* family (e.g. "filesystem", "terminal"), or nil.
func (c Config) Group(name string) []string {
	if c.Native == nil {
		return nil
	}
	return c.Native[name]
}

// ValidateApprovalReversibility checks every tool's approval class.
func (c Config) ValidateApprovalReversibility() error {
	if len(c.ApprovalReversibility) == 0 {
		return fmt.Errorf("approval_reversibility map is empty")
	}
	var missing []string
	for _, tool := range c.AllTools() {
		if _, ok := c.ApprovalReversibility[tool]; !ok {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("native tools missing approval_reversibility: %s", strings.Join(missing, ", "))
	}
	var bad []string
	for tool, tier := range c.ApprovalReversibility {
		switch tier {
		case TierReversible, TierRecoverable, TierIrreversible:
		default:
			bad = append(bad, fmt.Sprintf("%s=%q", tool, tier))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid approval_reversibility values: %s", strings.Join(bad, ", "))
	}
	return nil
}

// ValidateToolContracts checks the closed invocation-contract maps.
func (c Config) ValidateToolContracts() error {
	if err := c.ValidateApprovalReversibility(); err != nil {
		return err
	}
	var faults []string
	for tool := range c.ApprovalReversibility {
		owner, ok := c.Owner[tool]
		if !ok || strings.TrimSpace(owner) == "" {
			faults = append(faults, tool+" missing subsystem owner")
		}
		batch, ok := c.BatchPolicy[tool]
		if !ok {
			faults = append(faults, tool+" missing batch_policy")
		} else if !validBatchPolicy(batch) {
			faults = append(faults, fmt.Sprintf("%s batch_policy=%q", tool, batch))
		}
		lifecycle, ok := c.Lifecycle[tool]
		if !ok {
			faults = append(faults, tool+" missing lifecycle")
		} else if !validLifecycle(lifecycle) {
			faults = append(faults, fmt.Sprintf("%s lifecycle=%q", tool, lifecycle))
		}
	}
	for tool := range c.Owner {
		if _, ok := c.ApprovalReversibility[tool]; !ok {
			faults = append(faults, tool+" subsystem owner without approval_reversibility")
		}
	}
	for tool := range c.BatchPolicy {
		if _, ok := c.ApprovalReversibility[tool]; !ok {
			faults = append(faults, tool+" batch_policy without approval_reversibility")
		}
	}
	for tool, arg := range c.BatchOptInArg {
		batch, ok := c.BatchPolicy[tool]
		switch {
		case !ok:
			faults = append(faults, tool+" batch_opt_in_arg without batch_policy")
		case batch != BatchSameTool:
			faults = append(faults, tool+" batch_opt_in_arg requires same_tool batch_policy")
		}
		if strings.TrimSpace(arg) == "" {
			faults = append(faults, tool+" has empty batch_opt_in_arg")
		}
	}
	for tool, limit := range c.BatchConcurrencyLimit {
		batch, ok := c.BatchPolicy[tool]
		switch {
		case !ok:
			faults = append(faults, tool+" batch_concurrency_limit without batch_policy")
		case batch == BatchSerial:
			faults = append(faults, tool+" batch_concurrency_limit on serial tool")
		}
		if limit < 2 {
			faults = append(faults, fmt.Sprintf("%s batch_concurrency_limit=%d", tool, limit))
		}
	}
	for tool, arg := range c.BatchSerialWhenArg {
		batch, ok := c.BatchPolicy[tool]
		if !ok {
			faults = append(faults, tool+" batch_serial_when_arg without batch_policy")
		} else if batch != BatchSameTool {
			faults = append(faults, tool+" batch_serial_when_arg requires same_tool batch_policy")
		}
		arg = strings.TrimSpace(arg)
		if arg == "" {
			faults = append(faults, tool+" has empty batch_serial_when_arg")
		}
		if arg == strings.TrimSpace(c.BatchOptInArg[tool]) {
			faults = append(faults, fmt.Sprintf("%s batch_serial_when_arg equals opt-in arg %q", tool, arg))
		}
	}
	for tool, batch := range c.BatchPolicy {
		if batch == BatchSameTool && c.BatchConcurrencyLimit[tool] < 2 {
			faults = append(faults, tool+" same_tool batch_policy missing batch_concurrency_limit")
		}
	}
	for tool := range c.Lifecycle {
		if _, ok := c.ApprovalReversibility[tool]; !ok {
			faults = append(faults, tool+" lifecycle without approval_reversibility")
		}
	}
	for tool, order := range c.TurnOrder {
		if _, ok := c.ApprovalReversibility[tool]; !ok {
			faults = append(faults, tool+" turn_order without approval_reversibility")
		} else if !validTurnOrder(order) {
			faults = append(faults, fmt.Sprintf("%s turn_order=%q", tool, order))
		}
	}
	for tool, capabilities := range c.ExecutionCapabilities {
		if _, ok := c.ApprovalReversibility[tool]; !ok {
			faults = append(faults, tool+" execution_capabilities without approval_reversibility")
		}
		seen := map[string]struct{}{}
		for _, capability := range capabilities {
			if !validExecutionCapability(capability) {
				faults = append(faults, fmt.Sprintf("%s execution_capability=%q", tool, capability))
			}
			if _, duplicate := seen[capability]; duplicate {
				faults = append(faults, fmt.Sprintf("%s duplicate execution_capability=%q", tool, capability))
			}
			seen[capability] = struct{}{}
		}
	}
	if err := c.ValidateFamilies(); err != nil {
		faults = append(faults, err.Error())
	}
	if len(faults) > 0 {
		sort.Strings(faults)
		return fmt.Errorf("invalid tool contracts: %s", strings.Join(faults, "; "))
	}
	return nil
}

func validResourceFact(value string) bool {
	switch value {
	case ResourceFactCommandJobs, ResourceFactTerminals, ResourceFactPages, ResourceFactHeldCalls:
		return true
	default:
		return false
	}
}

// ValidateFamilies checks control-family membership and resource-implied facts.
func (c Config) ValidateFamilies() error {
	if len(c.Families) == 0 {
		return fmt.Errorf("families map is empty")
	}
	known := func(tool string) bool {
		if c.HasTool(tool) {
			return true
		}
		_, ok := c.ApprovalReversibility[tool]
		return ok
	}
	memberFamily := map[string]string{}
	var faults []string
	for family, members := range c.Families {
		family = strings.TrimSpace(family)
		if family == "" {
			faults = append(faults, "empty family id")
			continue
		}
		if len(members) == 0 {
			faults = append(faults, family+" has no members")
			continue
		}
		seen := map[string]struct{}{}
		for _, member := range members {
			member = strings.TrimSpace(member)
			if member == "" {
				faults = append(faults, family+" has an empty member")
				continue
			}
			if _, dup := seen[member]; dup {
				faults = append(faults, family+" duplicate member "+member)
				continue
			}
			seen[member] = struct{}{}
			if !known(member) {
				faults = append(faults, family+" unknown member "+member)
			}
			if other, ok := memberFamily[member]; ok {
				faults = append(faults, member+" in families "+other+" and "+family)
			}
			memberFamily[member] = family
		}
	}
	for tool, companions := range c.Companions {
		tool = strings.TrimSpace(tool)
		if !known(tool) {
			faults = append(faults, "companions unknown tool "+tool)
		}
		if len(companions) == 0 {
			faults = append(faults, "companions "+tool+" has no companions")
		}
		for _, companion := range companions {
			companion = strings.TrimSpace(companion)
			switch {
			case companion == "":
				faults = append(faults, "companions "+tool+" has an empty companion")
			case companion == tool:
				faults = append(faults, "companions "+tool+" names itself")
			case !known(companion):
				faults = append(faults, "companions "+tool+" unknown companion "+companion)
			}
		}
	}
	for fact, tools := range c.ResourceImplied {
		fact = strings.TrimSpace(fact)
		if !validResourceFact(fact) {
			faults = append(faults, "resource_implied unknown fact "+fact)
		}
		if len(tools) == 0 {
			faults = append(faults, "resource_implied "+fact+" has no tools")
		}
		for _, tool := range tools {
			tool = strings.TrimSpace(tool)
			if tool == "" {
				faults = append(faults, "resource_implied "+fact+" has an empty tool")
				continue
			}
			if !known(tool) {
				faults = append(faults, "resource_implied "+fact+" unknown tool "+tool)
			}
		}
	}
	if len(faults) > 0 {
		sort.Strings(faults)
		return fmt.Errorf("invalid families: %s", strings.Join(faults, "; "))
	}
	return nil
}

func validExecutionCapability(value string) bool {
	switch value {
	case "host_resource", "socket", "process_control", "host_execution", "direct_ip", "local_listen", "loopback_connect", "terminal_capture", "write_root", "read_path",
		"package_execution", "held_terminal_input", "file_change":
		return true
	default:
		return false
	}
}

func validTurnOrder(value string) bool {
	switch value {
	case TurnOrderNormal, TurnOrderLate, TurnOrderTerminal:
		return true
	default:
		return false
	}
}

func validBatchPolicy(value string) bool {
	switch value {
	case BatchSerial, BatchShared, BatchSameTool:
		return true
	default:
		return false
	}
}

func validLifecycle(value string) bool {
	switch value {
	case LifecycleReadOnly, LifecycleDBTransaction, LifecycleJournaledMutation,
		LifecycleDurableJob, LifecycleEffectAttempt, LifecycleEphemeralControl:
		return true
	default:
		return false
	}
}

// ValidateGrantTargetScopes validates scope values and referenced tools.
func (c Config) ValidateGrantTargetScopes(known func(string) bool) error {
	var bad []string
	for tool, scope := range c.GrantTargetScope {
		switch scope {
		case ScopeCommand, ScopePath, ScopeTool:
		default:
			bad = append(bad, fmt.Sprintf("%s=%q", tool, scope))
		}
		if known != nil && !known(tool) {
			bad = append(bad, fmt.Sprintf("%s (unknown tool)", tool))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid grant_target_scope entries: %s", strings.Join(bad, ", "))
	}
	return nil
}

// CommandScopedTools returns tools limited to exact-command approvals.
func (c Config) CommandScopedTools() []string {
	var out []string
	for tool, scope := range c.GrantTargetScope {
		if scope == ScopeCommand {
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	return out
}

// PathScopedTools returns sorted tool ids whose host grant predicate targets a path.
func (c Config) PathScopedTools() []string {
	var out []string
	for tool, scope := range c.GrantTargetScope {
		if scope == ScopePath {
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	return out
}

// WorkerBranchTools returns sorted tool ids that require a private write-worker branch.
func (c Config) WorkerBranchTools() []string {
	out := append([]string(nil), c.RequiresWorkerBranch...)
	sort.Strings(out)
	return out
}

// ProcessSpawningTools returns sorted tool ids that may create a subprocess.
func (c Config) ProcessSpawningTools() []string {
	out := append([]string(nil), c.SpawnsProcess...)
	sort.Strings(out)
	return out
}

// PathMutatingTools returns sorted tool ids that write a declared path.
func (c Config) PathMutatingTools() []string {
	out := append([]string(nil), c.MutatesPath...)
	sort.Strings(out)
	return out
}

// ValidateMutatesPath checks known tool names and uniqueness.
func (c Config) ValidateMutatesPath(known func(string) bool) error {
	if len(c.MutatesPath) == 0 {
		return fmt.Errorf("mutates_path is empty")
	}
	return validateToolList("mutates_path", c.MutatesPath, known)
}

// ContentMutatingTools returns sorted tool ids whose arguments carry authored
// file content.
func (c Config) ContentMutatingTools() []string {
	out := append([]string(nil), c.MutatesContent...)
	sort.Strings(out)
	return out
}

// ValidateMutatesContent validates content writers as path writers.
func (c Config) ValidateMutatesContent(known func(string) bool) error {
	if len(c.MutatesContent) == 0 {
		return fmt.Errorf("mutates_content is empty")
	}
	if err := validateToolList("mutates_content", c.MutatesContent, known); err != nil {
		return err
	}
	pathMutating := make(map[string]struct{}, len(c.MutatesPath))
	for _, tool := range c.MutatesPath {
		pathMutating[strings.TrimSpace(tool)] = struct{}{}
	}
	var orphans []string
	for _, tool := range c.MutatesContent {
		if _, ok := pathMutating[strings.TrimSpace(tool)]; !ok {
			orphans = append(orphans, tool)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return fmt.Errorf("mutates_content names %s, which mutates_path does not: "+
			"a tool that authors file content also writes a path",
			strings.Join(orphans, ", "))
	}
	return nil
}

// HasHostProducedTool reports whether name is a host-produced tool identity.
// These carry no schema, so HasTool does not find them.
func (c Config) HasHostProducedTool(name string) bool {
	name = strings.TrimSpace(name)
	for _, tool := range c.HostProducedTools {
		if tool == name {
			return true
		}
	}
	return false
}

// ValidateChunkablePayload checks known tool names and uniqueness.
func (c Config) ValidateChunkablePayload(known func(string) bool) error {
	return validateToolList("chunkable_payload", c.ChunkablePayload, known)
}

// ValidateSurveyNeutral checks known tool names and uniqueness.
func (c Config) ValidateSurveyNeutral(known func(string) bool) error {
	return validateToolList("survey_neutral", c.SurveyNeutral, known)
}

// ValidateBoundedInProcess checks known tool names and uniqueness.
func (c Config) ValidateBoundedInProcess(known func(string) bool) error {
	return validateToolList("bounded_in_process", c.BoundedInProcess, known)
}

// ValidateDetachAfterBudget checks known tool names and that each is read-only:
// an attempted or journaled effect cannot outlive the turn that records it.
func (c Config) ValidateDetachAfterBudget(known func(string) bool) error {
	if err := validateToolList("detach_after_budget", c.DetachAfterBudget, known); err != nil {
		return err
	}
	var bad []string
	for _, tool := range c.DetachAfterBudget {
		tool = strings.TrimSpace(tool)
		if c.Lifecycle[tool] != LifecycleReadOnly {
			bad = append(bad, tool)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("detach_after_budget entries must be read_only: %s", strings.Join(bad, ", "))
	}
	return nil
}

// ValidateSocketArg checks known tool names and non-empty argument names.
func (c Config) ValidateSocketArg(known func(string) bool) error {
	for tool, arg := range c.SocketArg {
		if !known(strings.TrimSpace(tool)) {
			return fmt.Errorf("socket_arg: unknown tool %q", tool)
		}
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("socket_arg[%s]: empty argument name", tool)
		}
	}
	return nil
}

// ValidateSecretReferenceSurface checks known tool names and closed surface values.
func (c Config) ValidateSecretReferenceSurface(known func(string) bool) error {
	for tool, surface := range c.SecretReferenceSurface {
		if !known(strings.TrimSpace(tool)) {
			return fmt.Errorf("secret_reference_surface: unknown tool %q", tool)
		}
		switch strings.TrimSpace(surface) {
		case SecretSurfaceCommand, SecretSurfaceTerminal, SecretSurfaceHTTPRequest, SecretSurfaceFile:
		default:
			return fmt.Errorf("secret_reference_surface[%s]: unknown surface %q", tool, surface)
		}
	}
	return nil
}

// ValidateSecretReferenceArgs verifies that argument-scoped secret resolution is configured
// only for known tools on the file secret-reference surface.
func (c Config) ValidateSecretReferenceArgs(known func(string) bool) error {
	for tool, args := range c.SecretReferenceArgs {
		tool = strings.TrimSpace(tool)
		if !known(tool) {
			return fmt.Errorf("secret_reference_args: unknown tool %q", tool)
		}
		if c.SecretReferenceSurface[tool] != SecretSurfaceFile {
			return fmt.Errorf("secret_reference_args[%s]: tool must have secret_reference_surface: %s", tool, SecretSurfaceFile)
		}
		if len(args) == 0 {
			return fmt.Errorf("secret_reference_args[%s]: requires at least one argument slot", tool)
		}
	}
	return nil
}

// sessionScopeValues is the closed vocabulary of session shapes.
var sessionScopeValues = []string{"worker_child", "addressed_session"}

// ValidateSessionScope checks known tool names and closed scope values.
func (c Config) ValidateSessionScope(known func(string) bool) error {
	allowed := make(map[string]bool, len(sessionScopeValues))
	for _, v := range sessionScopeValues {
		allowed[v] = true
	}
	for tool, scope := range c.SessionScope {
		if !known(strings.TrimSpace(tool)) {
			return fmt.Errorf("session_scope: unknown tool %q", tool)
		}
		if !allowed[strings.TrimSpace(scope)] {
			return fmt.Errorf("session_scope[%s]: scope %q not in %s",
				tool, scope, strings.Join(sessionScopeValues, ", "))
		}
	}
	return nil
}

// ValidateSpawnsProcess checks known tool names and uniqueness.
func (c Config) ValidateSpawnsProcess(known func(string) bool) error {
	return validateToolList("spawns_process", c.SpawnsProcess, known)
}

// validateToolList rejects empty, duplicate, and unknown tools.
func validateToolList(field string, tools []string, known func(string) bool) error {
	seen := map[string]struct{}{}
	var bad []string
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			bad = append(bad, "(empty)")
			continue
		}
		if _, duplicate := seen[tool]; duplicate {
			bad = append(bad, tool+" (duplicate)")
			continue
		}
		seen[tool] = struct{}{}
		if known != nil && !known(tool) {
			bad = append(bad, tool+" (unknown tool)")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid %s entries: %s", field, strings.Join(bad, ", "))
	}
	return nil
}

// ValidateGrantIdentityNeutralArgs validates ignored grant-identity arguments.
func (c Config) ValidateGrantIdentityNeutralArgs(known func(string) bool) error {
	var bad []string
	for tool, args := range c.GrantIdentityNeutralArgs {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			bad = append(bad, "(empty tool)")
			continue
		}
		if known != nil && !known(tool) {
			bad = append(bad, tool+" (unknown tool)")
		}
		if len(args) == 0 {
			bad = append(bad, tool+" (no arguments)")
		}
		seen := map[string]struct{}{}
		for _, arg := range args {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				bad = append(bad, tool+" (empty argument)")
				continue
			}
			if _, dup := seen[arg]; dup {
				bad = append(bad, tool+" duplicate argument "+arg)
				continue
			}
			seen[arg] = struct{}{}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid grant_identity_neutral_args entries: %s", strings.Join(bad, ", "))
	}
	return nil
}

// GrantIdentityNeutralArgRows returns the sorted tool → sorted-argument table.
func (c Config) GrantIdentityNeutralArgRows() []GrantIdentityNeutralArgRow {
	rows := make([]GrantIdentityNeutralArgRow, 0, len(c.GrantIdentityNeutralArgs))
	for tool, args := range c.GrantIdentityNeutralArgs {
		sorted := append([]string(nil), args...)
		sort.Strings(sorted)
		rows = append(rows, GrantIdentityNeutralArgRow{Tool: tool, Args: sorted})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Tool < rows[j].Tool })
	return rows
}

// GrantIdentityNeutralArgRow is one tool's authority-neutral argument set.
type GrantIdentityNeutralArgRow struct {
	Tool string
	Args []string
}

// ValidateRequiresWorkerBranch checks known tool names and uniqueness.
func (c Config) ValidateRequiresWorkerBranch(known func(string) bool) error {
	seen := map[string]struct{}{}
	var bad []string
	for _, tool := range c.RequiresWorkerBranch {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			bad = append(bad, "(empty)")
			continue
		}
		if _, dup := seen[tool]; dup {
			bad = append(bad, tool+" (duplicate)")
			continue
		}
		seen[tool] = struct{}{}
		if known != nil && !known(tool) {
			bad = append(bad, tool+" (unknown tool)")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid requires_worker_branch entries: %s", strings.Join(bad, ", "))
	}
	return nil
}

// ToolsByTier returns sorted tool ids for each locked tier value.
func (c Config) ToolsByTier() (reversible, recoverable, irreversible []string) {
	for tool, tier := range c.ApprovalReversibility {
		switch tier {
		case TierReversible:
			reversible = append(reversible, tool)
		case TierRecoverable:
			recoverable = append(recoverable, tool)
		case TierIrreversible:
			irreversible = append(irreversible, tool)
		}
	}
	sort.Strings(reversible)
	sort.Strings(recoverable)
	sort.Strings(irreversible)
	return reversible, recoverable, irreversible
}

// FamilyIDs returns sorted control-family identifiers.
func (c Config) FamilyIDs() []string {
	out := make([]string, 0, len(c.Families))
	for id := range c.Families {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// FamilyMembers returns a copy of one family's members in catalog order.
func (c Config) FamilyMembers(id string) []string {
	return append([]string(nil), c.Families[id]...)
}

// CompanionTools returns the sorted tools that declare companions.
func (c Config) CompanionTools() []string {
	out := make([]string, 0, len(c.Companions))
	for tool := range c.Companions {
		out = append(out, tool)
	}
	sort.Strings(out)
	return out
}

// CompanionsOf returns a copy of one tool's companions in catalog order.
func (c Config) CompanionsOf(tool string) []string {
	return append([]string(nil), c.Companions[tool]...)
}

// ResourceImpliedFacts returns sorted live-resource fact keys.
func (c Config) ResourceImpliedFacts() []string {
	out := make([]string, 0, len(c.ResourceImplied))
	for fact := range c.ResourceImplied {
		out = append(out, fact)
	}
	sort.Strings(out)
	return out
}

// ResourceImpliedTools returns a copy of the control tools for one live-resource fact.
func (c Config) ResourceImpliedTools(fact string) []string {
	return append([]string(nil), c.ResourceImplied[fact]...)
}

// ToolContractRows returns the exact sorted runtime contract table.
func (c Config) ToolContractRows() []ToolContractRow {
	chunkable := make(map[string]bool, len(c.ChunkablePayload))
	for _, tool := range c.ChunkablePayload {
		chunkable[strings.TrimSpace(tool)] = true
	}
	neutral := make(map[string]bool, len(c.SurveyNeutral))
	for _, tool := range c.SurveyNeutral {
		neutral[strings.TrimSpace(tool)] = true
	}
	detach := make(map[string]bool, len(c.DetachAfterBudget))
	for _, tool := range c.DetachAfterBudget {
		detach[strings.TrimSpace(tool)] = true
	}
	bounded := make(map[string]bool, len(c.BoundedInProcess))
	for _, tool := range c.BoundedInProcess {
		bounded[strings.TrimSpace(tool)] = true
	}
	rows := make([]ToolContractRow, 0, len(c.ApprovalReversibility))
	for tool := range c.ApprovalReversibility {
		rows = append(rows, ToolContractRow{
			Tool:                  tool,
			Owner:                 c.Owner[tool],
			Reversibility:         c.ApprovalReversibility[tool],
			Batch:                 c.BatchPolicy[tool],
			BatchOptInArg:         strings.TrimSpace(c.BatchOptInArg[tool]),
			BatchConcurrencyLimit: c.BatchConcurrencyLimit[tool],
			BatchSerialWhenArg:    strings.TrimSpace(c.BatchSerialWhenArg[tool]),
			TurnOrder:             c.TurnOrder[tool],
			Lifecycle:             c.Lifecycle[tool],
			Capabilities:          append([]string(nil), c.ExecutionCapabilities[tool]...),

			ChunkablePayload:  chunkable[tool],
			SurveyNeutral:     neutral[tool],
			DetachAfterBudget: detach[tool],
			SessionScope:      strings.TrimSpace(c.SessionScope[tool]),
			SocketArg:         strings.TrimSpace(c.SocketArg[tool]),
			BoundedInProcess:  bounded[tool],

			SecretReferenceSurface: strings.TrimSpace(c.SecretReferenceSurface[tool]),
			SecretReferenceArgs:    append([]string(nil), c.SecretReferenceArgs[tool]...),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Tool < rows[j].Tool })
	return rows
}

// ToolContractRow is one generated tool-invocation contract.
type ToolContractRow struct {
	Tool          string
	Owner         string
	Reversibility string
	Batch         string
	BatchOptInArg string
	// BatchConcurrencyLimit caps a concurrent run below the host ceiling.
	BatchConcurrencyLimit int
	// BatchSerialWhenArg disables sibling execution when this argument is present.
	BatchSerialWhenArg string
	TurnOrder          string
	Lifecycle          string
	Capabilities       []string
	// ChunkablePayload marks a tool whose arguments can be split across calls.
	ChunkablePayload bool
	// SurveyNeutral marks a bookkeeping tool that neither observes nor acts.
	SurveyNeutral bool
	// DetachAfterBudget marks a read-only tool whose call may outlive its foreground wait.
	DetachAfterBudget bool
	// SessionScope is the session shape this tool belongs to, empty for either.
	SessionScope string
	// SocketArg names the argument whose AF_UNIX socket the tool dials itself.
	SocketArg string
	// BoundedInProcess marks a tool that executes synchronously in-process.
	BoundedInProcess bool
	// SecretReferenceSurface names the outbound screen for resolved managed-secret references.
	SecretReferenceSurface string
	// SecretReferenceArgs names the value slots resolved when SecretReferenceSurface is SecretSurfaceFile.
	SecretReferenceArgs []string
}

// argvHostRunnerTools lists catalog argv runners for host prompts.
var argvHostRunnerTools = []string{"command", "verify"}

// ArgvHostRunnerTools returns a copy of the argv host-runner tool names.
func ArgvHostRunnerTools() []string {
	out := make([]string, len(argvHostRunnerTools))
	copy(out, argvHostRunnerTools)
	return out
}

// multiRootCapabilities is the closed set of multi-root capability ids, ordered
// from no path surface to the widest.
var multiRootCapabilities = []string{"none", "path", "discovery", "command"}

// MultiRootCapabilities returns the capability ids in order.
func MultiRootCapabilities() []string {
	out := make([]string, len(multiRootCapabilities))
	copy(out, multiRootCapabilities)
	return out
}

// MultiRootTools returns tool id -> multi-root capability id.
func (c Config) MultiRootTools() map[string]string {
	out := make(map[string]string)
	for capability, tools := range c.MultiRoot {
		for _, tool := range tools {
			out[strings.TrimSpace(tool)] = strings.TrimSpace(capability)
		}
	}
	return out
}

// ValidateMultiRoot rejects unknown or duplicate tool classifications.
func (c Config) ValidateMultiRoot(known func(string) bool) error {
	if len(c.MultiRoot) == 0 {
		return fmt.Errorf("multi_root is empty")
	}
	valid := make(map[string]struct{}, len(multiRootCapabilities))
	for _, id := range multiRootCapabilities {
		valid[id] = struct{}{}
	}
	capabilities := make([]string, 0, len(c.MultiRoot))
	for capability := range c.MultiRoot {
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)

	assigned := map[string]string{}
	var bad []string
	for _, capability := range capabilities {
		if _, ok := valid[strings.TrimSpace(capability)]; !ok {
			bad = append(bad, fmt.Sprintf("%s (unknown capability; want one of %s)",
				capability, strings.Join(multiRootCapabilities, ", ")))
			continue
		}
		if err := validateToolList("multi_root."+capability, c.MultiRoot[capability], known); err != nil {
			return err
		}
		for _, tool := range c.MultiRoot[capability] {
			tool = strings.TrimSpace(tool)
			if prior, dup := assigned[tool]; dup {
				bad = append(bad, fmt.Sprintf("%s (classified %s and %s)", tool, prior, capability))
				continue
			}
			assigned[tool] = capability
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid multi_root entries: %s", strings.Join(bad, ", "))
	}
	// Completeness across the whole boot surface needs the registry and stays a
	// contract test; a tool this file declares is one it can insist on here.
	var unclassified []string
	for _, tool := range c.NativeToolNames() {
		if _, ok := assigned[tool]; !ok {
			unclassified = append(unclassified, tool)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		return fmt.Errorf("native tools missing from multi_root: %s", strings.Join(unclassified, ", "))
	}
	return nil
}
