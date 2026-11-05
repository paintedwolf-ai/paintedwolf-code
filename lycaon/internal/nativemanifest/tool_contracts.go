package nativemanifest

import (
	"fmt"
	"sort"
	"strings"

)

// ValidateToolContracts checks the closed invocation-contract maps.
func (c Config) ValidateToolContracts() error {
	if err := c.ValidateApprovalReversibility(); err != nil {
		return err
	}
	var faults []string
	for _, tool := range c.classifiedNames() {
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
		if _, ok := c.Reversibility(tool); !ok {
			faults = append(faults, tool+" subsystem owner without approval_reversibility")
		}
	}
	for tool := range c.BatchPolicy {
		if _, ok := c.Reversibility(tool); !ok {
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
		if _, ok := c.Reversibility(tool); !ok {
			faults = append(faults, tool+" lifecycle without approval_reversibility")
		}
	}
	for tool, order := range c.TurnOrder {
		if _, ok := c.Reversibility(tool); !ok {
			faults = append(faults, tool+" turn_order without approval_reversibility")
		} else if !validTurnOrder(order) {
			faults = append(faults, fmt.Sprintf("%s turn_order=%q", tool, order))
		}
	}
	for tool, capabilities := range c.ExecutionCapabilities {
		if _, ok := c.Reversibility(tool); !ok {
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
	names := c.classifiedNames()
	rows := make([]ToolContractRow, 0, len(names))
	for _, tool := range names {
		tier, _ := c.Reversibility(tool)
		rows = append(rows, ToolContractRow{
			Tool:                  tool,
			Owner:                 c.Owner[tool],
			Reversibility:         tier,
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
