const DISCOVERY_TOOLS = new Set(["request_tools", "skills_read"]);

/** A `need` carrying a discovery cursor browses the catalog rather than naming a tool. */
export function isDiscoveryRequest(tool: string, args: Record<string, unknown> | undefined): boolean {
  return DISCOVERY_TOOLS.has(tool.toLowerCase()) && typeof args?.need === "string" && args.need.trim().startsWith("discovery:");
}
