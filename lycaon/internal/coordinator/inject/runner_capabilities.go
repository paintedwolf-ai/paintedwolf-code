package inject

import "github.com/lycaon/lycaon/internal/toolcontract"

func mergeRunnerCapabilityVars(offered []string, vars map[string]any) {
	capabilities := map[string]toolcontract.Capability{
		"host_resource":    toolcontract.CapabilityHostResource,
		"socket":           toolcontract.CapabilitySocket,
		"direct_ip":        toolcontract.CapabilityDirectIP,
		"local_listen":     toolcontract.CapabilityLocalListen,
		"loopback_connect": toolcontract.CapabilityLoopbackConnect,
		"write_root":       toolcontract.CapabilityWriteRoot,
		"read_path":        toolcontract.CapabilityReadPath,
	}
	for name, capability := range capabilities {
		var tools []string
		for _, offeredTool := range offered {
			switch offeredTool {
			case "command", "verify", "terminal_open":
				contract, ok := toolcontract.Lookup(offeredTool)
				if ok && contract.Supports(capability) {
					tools = append(tools, offeredTool)
				}
			}
		}
		vars["runner_"+name+"_tools"] = tools
	}
}
