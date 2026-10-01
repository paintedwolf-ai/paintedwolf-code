import { describe, expect, it } from "vitest";
import { discoverySections, hasDiscoveryOutput } from "./discovery.ts";
import { buildStructuredToolPresentation } from "../tool-part-structured.ts";
import type { ToolPartView } from "../tool-part-model.ts";

const discovery = {
  status: "ranking_unavailable", failure: "timeout",
  entries: [{ name: "available-entry", description: "A useful procedure" }],
  total: 21, next_need: "opaque-cursor",
};

describe("discovery catalog presentation", () => {
  it.each(["request_tools", "skills_read"])("shows %s catalogs without claiming activation", (tool) => {
    const part: ToolPartView = {
      id: "d", toolCallId: "d", assistantMessageId: "a", messageId: "m", tool,
      kind: tool === "skills_read" ? "skill" : "generic", status: "completed",
      args: { need: "discovery:opaque-cursor" }, output: JSON.stringify({ discovery }),
    };
    const view = buildStructuredToolPresentation(part);
    expect(view.sections).toContainEqual({ kind: "facts", facts: [{ label: "available-entry", value: "A useful procedure", wide: true }] });
    const text = JSON.stringify(view.sections);
    expect(text).toContain("Local AI timed out");
    expect(text).toContain("Showing 1 of 21");
    expect(text).not.toContain("Nothing loaded");
    expect(text).not.toContain("opaque-cursor");
    expect(JSON.stringify(view.argsFacts)).not.toContain("opaque-cursor");
  });

  it("keeps selected tools alongside an unanswered remainder", () => {
    const part: ToolPartView = {
      id: "p", toolCallId: "p", assistantMessageId: "a", messageId: "m",
      tool: "request_tools", kind: "generic", status: "completed",
      output: JSON.stringify({ loaded: ["read"], discovery }),
    };
    const text = JSON.stringify(buildStructuredToolPresentation(part).sections);
    expect(text).toContain('"label":"Loaded","value":"read"');
    expect(text).toContain("available-entry");
  });

  it("distinguishes no match, browsing, and outage without exposing cursor plumbing", () => {
    expect(discoverySections({ discovery: { ...discovery, status: "no_match" } })).toEqual([]);
    expect(JSON.stringify(discoverySections({ discovery: { ...discovery, status: "catalog" } }))).toContain("Available catalog");
    expect(hasDiscoveryOutput(`[read#1]\n${JSON.stringify({ discovery })}`)).toBe(true);
    expect(hasDiscoveryOutput("Skill: available-entry\nInstructions")).toBe(false);
    expect(hasDiscoveryOutput('{"discovery":{"status":"unknown","entries":[]}}')).toBe(false);
  });
});
