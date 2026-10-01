import { describe, expect, it } from "vitest";
import type { SecurityOverview, SecurityScannerState } from "../api/types.ts";
import {
  absenceSentence,
  anyPassSuperseded,
  ignoreSentence,
  ledgerAllCount,
  ledgerCountsSummary,
  ledgerEmptyLine,
  ledgerOpenCount,
  ledgerStateIsAbsent,
  scannerReadinessLine,
} from "./ledger-display.ts";

function scanner(overrides: Partial<SecurityScannerState> = {}): SecurityScannerState {
  return {
    id: "opengrep-sast",
    label: "Static analysis",
    categories: ["sast"],
    available: true,
    running: false,
    watching: true,
    pass_superseded: false,
    ...overrides,
  };
}

function overview(overrides: Partial<SecurityOverview> = {}): SecurityOverview {
  return {
    project_id: "proj-1",
    enabled: true,
    scanners: [scanner()],
    introduced_since_baseline: 0,
    fixed_since_baseline: 0,
    ...overrides,
  };
}

describe("ledger presentation", () => {
  it("treats every absence state as absence", () => {
    expect(ledgerStateIsAbsent("open")).toBe(false);
    expect(ledgerStateIsAbsent("reopened")).toBe(false);
    for (const state of ["fixed", "not_observed", "unverified"] as const) {
      expect(ledgerStateIsAbsent(state)).toBe(true);
    }
  });

  it("never presents an unobserved path as a fix", () => {
    const bounded = absenceSentence("not_observed", {
      observed_at: "2026-09-09T10:00:00Z",
      coverage_status: "bounded",
      execution_moved: false,
    });
    expect(bounded).toContain("bounded");
    expect(bounded).toContain("establishes nothing");
    expect(bounded).not.toMatch(/\bfixed\b/);

    const partial = absenceSentence("not_observed", {
      observed_at: "2026-09-09T10:00:00Z",
      coverage_status: "partial",
      execution_moved: false,
    });
    expect(partial).toContain("establishes nothing");
  });

  it("says a fix is evidence of a change, not of correctness", () => {
    const sentence = absenceSentence("fixed", {
      observed_at: "2026-09-09T10:00:00Z",
      coverage_status: "complete",
      execution_moved: false,
    });
    expect(sentence).toContain("evidence of a change");
    expect(sentence).toContain("not proof");
  });

  it("names a moved engine as the reason an absence is unverified", () => {
    const moved = absenceSentence("unverified", {
      observed_at: "2026-09-09T10:00:00Z",
      coverage_status: "complete",
      execution_moved: true,
    });
    expect(moved).toContain("no longer applies");
    expect(moved).toContain("full pass");
  });

  it("says nothing about presence states", () => {
    expect(absenceSentence("open", undefined)).toBe("");
    expect(absenceSentence("reopened", undefined)).toBe("");
  });

  it("reads a quiet scanner as watching, never as overdue", () => {
    const line = scannerReadinessLine(scanner({ last_full_at: "2026-09-10T09:00:00Z" }));
    expect(line).toContain("Watching for changes");
    expect(line).not.toContain("next");
    expect(line).not.toContain("overdue");
  });

  it("flags a scanner whose engine moved after a pass it had established", () => {
    const moved = scanner({ pass_superseded: true, last_full_at: "2026-09-10T09:00:00Z" });
    expect(scannerReadinessLine(moved)).toContain("engine changed since that pass");
    expect(anyPassSuperseded(overview({ scanners: [moved] }))).toBe(true);
    expect(anyPassSuperseded(overview())).toBe(false);
  });

  // No full pass means nothing to supersede.
  it("says nothing about a superseded pass when there was no pass", () => {
    const never = scanner({ pass_superseded: true, last_full_at: undefined });
    expect(scannerReadinessLine(never)).not.toContain("engine changed");
    expect(scannerReadinessLine(never)).toContain("not scanned yet");
    expect(anyPassSuperseded(overview({ scanners: [never] }))).toBe(false);
  });

  it("says a running scanner is scanning, not waiting", () => {
    expect(scannerReadinessLine(scanner({ running: true }))).toBe("Scanning now");
  });

  it("keeps an unavailable scanner's reason instead of inventing a state", () => {
    const line = scannerReadinessLine(
      scanner({ available: false, unavailable_reason: "engine binary missing" }),
    );
    expect(line).toBe("engine binary missing");
  });

  it("keeps fixed apart from unverified and not observed in the summary", () => {
    const summary = ledgerCountsSummary({
      open: 2,
      reopened: 1,
      fixed: 4,
      not_observed: 3,
      unverified: 5,
      ignored: 6,
    });
    expect(summary).toContain("2 open");
    expect(summary).toContain("1 reopened");
    expect(summary).toContain("4 fixed");
    expect(summary).toContain("5 unverified");
    expect(summary).toContain("3 not observed");
    expect(summary).toContain("6 ignored");
  });

  it("omits the zero counts that would only add noise", () => {
    const summary = ledgerCountsSummary({
      open: 3,
      reopened: 0,
      fixed: 1,
      not_observed: 0,
      unverified: 0,
      ignored: 0,
    });
    expect(summary).toBe("3 open · 1 fixed");
  });

  // Open counts open and reopened; All counts every state.
  it("counts the open tab without what was decided or what left", () => {
    const counts = {
      open: 3,
      reopened: 1,
      fixed: 4,
      not_observed: 2,
      unverified: 5,
      ignored: 6,
    };
    expect(ledgerOpenCount(counts)).toBe(4);
    expect(ledgerAllCount(counts)).toBe(21);
  });

  it("says why a finding came back when its ignore lapsed", () => {
    const sentence = ignoreSentence({
      entry_id: "e1",
      reason: "was meant to be temporary",
      expires_on: "2026-01-01",
      expired: true,
    });
    expect(sentence).toContain("lapsed");
    expect(sentence).toContain("open again");
    expect(sentence).toContain("was meant to be temporary");
  });

  it("names the predicate a live decision matched on", () => {
    const sentence = ignoreSentence({
      entry_id: "e1",
      reason: "fixture material",
      matched_on: "path: test/**",
      expires_on: "2026-12-09",
    });
    expect(sentence).toContain("fixture material.");
    expect(sentence).toContain("path: test/**");
    expect(sentence).toContain("2026-12-09");
  });

  // Existing terminal punctuation is kept.
  it("does not double a reason's own full stop", () => {
    const sentence = ignoreSentence({
      entry_id: "e1",
      reason: "Reviewed with the platform team.",
      matched_on: "kind: secret",
    });
    expect(sentence).toContain("platform team. Matched on");
    expect(sentence).not.toContain("team..");
  });
});

describe("ledgerEmptyLine", () => {
  const counts = { open: 0, reopened: 0, fixed: 2, not_observed: 0, unverified: 0, ignored: 0 };
  const pass = (coverage_status: "complete" | "bounded") => ({
    assessment_id: "a1",
    requested_at: "2026-09-01T09:00:00Z",
    started_at: "2026-09-01T09:00:00Z",
    completed_at: "2026-09-01T09:10:00Z",
    coverage_status,
    members: [],
  });

  it("scopes an empty list to what the scans covered", () => {
    expect(ledgerEmptyLine(counts, overview({ last_full: undefined }))).toBe(
      "No open findings reported. No full scan has run, so only changed files have been scanned.",
    );
    expect(ledgerEmptyLine(counts, overview({ last_full: pass("bounded") }))).toBe(
      "No open findings reported. The last full scan did not cover the whole project.",
    );
    expect(ledgerEmptyLine(counts, overview({ last_full: pass("complete") }))).toBe(
      "No open findings reported.",
    );
  });

  it("says nothing is recorded when the ledger is empty", () => {
    expect(ledgerEmptyLine(undefined, overview({ last_full: undefined }))).toBe(
      "No findings recorded yet. No full scan has run, so only changed files have been scanned.",
    );
  });
});
