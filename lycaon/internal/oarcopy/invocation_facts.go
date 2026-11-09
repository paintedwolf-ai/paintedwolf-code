package oarcopy

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// PublishInvocationRecovery binds remedies to actual tool identity and arguments.
func PublishInvocationRecovery(facts map[string]any, tool string, args map[string]any, snapshot *toolcontract.ToolCapabilitySnapshot) {
	facts["paintedwolf.command_surface"] = tool == "command"
	facts["paintedwolf.syntax_override_commands"] = strings.Join(syntaxOverrideCommands(tool), "`, `")
	schemaTool, _ := facts["paintedwolf.argument_schema_tool"].(string)
	facts["paintedwolf.argument_schema_matches_tool"] = schemaTool != "" && schemaTool == tool
	contract, known := toolcontract.Lookup(tool)
	if snapshot != nil {
		contract = toolcontract.Contract{Capabilities: snapshot.Capabilities}
		known = snapshot.Tool == tool
	}
	fields := contract.CapabilityRequestFields()
	if !known {
		fields = nil
	}
	facts["paintedwolf.capability_contract_known"] = known
	facts["paintedwolf.capability_request_fields"] = append([]string{}, fields...)
	request, _ := args["capability_request"].(map[string]any)
	for _, field := range toolcontract.CapabilityRequestFields() {
		supported := known && slices.Contains(fields, field)
		facts["paintedwolf.supports_"+field] = supported
		_, requested := request[field]
		facts["paintedwolf.missing_capability_"+field] = supported && args != nil && !requested
	}
}
