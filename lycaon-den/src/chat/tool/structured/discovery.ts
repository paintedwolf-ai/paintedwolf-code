import { isRecord } from "../../../utils/type-guards.ts";
import { parseToolJsonObject, type StructuredToolSection } from "../tool-presentation-contract.ts";

const DISCOVERY_TOOLS = new Set(["request_tools", "skills_read"]);

/** A `need` carrying a discovery cursor browses the catalog rather than naming a tool. */
export function isDiscoveryRequest(tool: string, args: Record<string, unknown> | undefined): boolean {
  return DISCOVERY_TOOLS.has(tool.toLowerCase()) && typeof args?.need === "string" && args.need.trim().startsWith("discovery:");
}

type DiscoveryPage = Record<string, unknown> & { entries: unknown[] };

function discoveryPage(parsed: Record<string, unknown>): DiscoveryPage | null {
  const page = parsed.discovery;
  if (!isRecord(page) || !Array.isArray(page.entries)) return null;
  return page.status === "catalog" || page.status === "ranking_unavailable" ? { ...page, entries: page.entries } : null;
}

/** Discovery has its own outcome even when the resolution receipt has no names. */
export function hasDiscoveryOutput(output: string): boolean {
  const wire = parseToolJsonObject(output);
  return wire !== null && discoveryPage(wire.parsed) !== null;
}

function discoveryLabel(page: Record<string, unknown>): string {
  if (page.status === "catalog") return "Available catalog";
  switch (page.failure) {
    case "timeout": return "Local AI timed out; available entries are listed for selection.";
    case "disabled": return "Local AI is disabled; available entries are listed for selection.";
    case "fault": return "Local AI could not complete ranking; available entries are listed for selection.";
    default: return "Local AI is unavailable; available entries are listed for selection.";
  }
}

export function discoverySections(parsed: Record<string, unknown>): StructuredToolSection[] {
  const page = discoveryPage(parsed);
  if (!page) return [];
  const facts = page.entries.filter(isRecord).flatMap((entry) =>
    typeof entry.name === "string" && typeof entry.description === "string"
      ? [{ label: entry.name, value: entry.description, wide: true }]
      : [],
  );
  const sections: StructuredToolSection[] = [{ kind: "note", text: discoveryLabel(page) }];
  if (facts.length) sections.push({ kind: "facts", facts });
  if (typeof page.total === "number") {
    sections.push({ kind: "note", text: `Showing ${facts.length} of ${page.total} available entries.${page.next_need ? " More entries are available." : ""}` });
  }
  return sections;
}
