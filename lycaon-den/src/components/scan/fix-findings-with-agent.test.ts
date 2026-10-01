import { describe, expect, it } from "vitest";
import type { FindingLedgerEntry } from "../../api/types.ts";
import { fixFindingsPrompt } from "./fix-findings-with-agent.ts";

function entry(rule: string, path: string, line: number): FindingLedgerEntry {
  return {
    finding: {
      rule_id: rule,
      level: "high",
      message: `${rule} is unsafe here`,
      locations: [{ uri: path, start_line: line }],
      fingerprints: { primary: `fp-${rule}` },
      tool: { driver_id: "opengrep-sast", name: "opengrep" },
    },
    state: "open",
    scanner_id: "opengrep-sast",
    first_seen_at: "2026-09-01T10:00:00Z",
    last_seen_at: "2026-09-10T10:00:00Z",
    observations: 1,
  } as unknown as FindingLedgerEntry;
}

describe("fix findings with agent", () => {
  // The draft names each finding's line.
  it("names the location of a single finding", () => {
    const prompt = fixFindingsPrompt([entry("sql-concat", "internal/db/query.go", 12)]);
    expect(prompt).toContain("internal/db/query.go:12");
    expect(prompt).toContain("sql-concat");
  });

  it("lists every location when several were chosen", () => {
    const prompt = fixFindingsPrompt([
      entry("sql-concat", "internal/db/query.go", 12),
      entry("sql-concat", "internal/db/other.go", 40),
    ]);
    expect(prompt).toContain("2 findings from sql-concat");
    expect(prompt).toContain("- internal/db/query.go:12");
    expect(prompt).toContain("- internal/db/other.go:40");
  });

  it("says how many rules are involved when they differ", () => {
    const prompt = fixFindingsPrompt([
      entry("sql-concat", "a.go", 1),
      entry("weak-hash", "b.go", 2),
    ]);
    expect(prompt).toContain("2 security findings across 2 rules");
  });
});
