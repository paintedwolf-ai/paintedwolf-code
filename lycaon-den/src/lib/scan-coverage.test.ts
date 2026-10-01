import { describe, expect, it } from "vitest";
import type { CodeScan, SecurityFullPass, SecurityFullPassMember, SecurityOverview } from "../api/types.ts";
import {
  coverageChipState,
  coverageSentence,
  defaultFullScanSelection,
  filterFindingsNewSince,
  fullPassMemberMeasured,
  fullPassMemberReadout,
  fullPassWaitingOn,
  fullPassMemberStatus,
  fullScanToggleLabel,
  fullScanProgressReadout,
  fullScanScanStatus,
  securityBaselineTime,
} from "./scan-coverage.ts";

const NOW = Date.parse("2026-09-09T12:00:00Z");

const baseOverview: SecurityOverview = {
  project_id: "proj-1",
  enabled: true,
  baseline: {
    snapshot_id: "snap-1",
    created_at: "2026-09-09T09:41:00Z",
    file_count: 12_702,
    unobserved_directories: 2,
  },
  scanners: [
    { id: "sast", label: "Static analysis", categories: ["sast"], available: true, running: false, watching: true, pass_superseded: false },
    { id: "sca", label: "Supply chain", categories: ["sca"], available: false, unavailable_reason: "No lockfile", running: false, watching: false, pass_superseded: false },
  ],
  introduced_since_baseline: 1,
  fixed_since_baseline: 0,
};

const runningScan: CodeScan = {
  id: "scan-run",
  categories: ["sast"],
  scanner_id: "sast",
  status: "running",
  long_running: false,
  findings_count: 0,
  created_at: "2026-09-09T11:00:00Z",
  progress: { chunks: 7, completed: 4, files: 12_702 },
};

describe("coverageSentence", () => {
  it("reads incremental while no full pass exists", () => {
    const line = coverageSentence(baseOverview, NOW);
    expect(line).toMatch(/^Incremental since \d/);
    expect(line).toContain("· no full scan yet");
  });

  it("reads complete from the last pass", () => {
    const line = coverageSentence(
      { ...baseOverview, last_full: { assessment_id: "a1", requested_at: "t", members: [], started_at: "2026-09-01T13:00:00Z", completed_at: "2026-09-01T14:02:00Z", coverage_status: "complete" } },
      NOW,
    );
    expect(line).toMatch(/^Complete as of Sep 1/);
    expect(line).not.toContain("·");
  });

  it("qualifies bounded and partial coverage without guessing the cause", () => {
    const bounded = coverageSentence(
      { ...baseOverview, last_full: { assessment_id: "a1", requested_at: "t", members: [], started_at: "2026-09-08T13:00:00Z", completed_at: "2026-09-08T14:02:00Z", coverage_status: "bounded" } },
      NOW,
    );
    expect(bounded).toMatch(/^Bounded as of Tue/);
    expect(bounded).toContain("· some directories were not analyzed");
    const partial = coverageSentence(
      { ...baseOverview, last_full: { assessment_id: "a1", requested_at: "t", members: [], started_at: "2026-09-08T13:00:00Z", completed_at: "2026-09-08T14:02:00Z", coverage_status: "partial" } },
      NOW,
    );
    expect(partial).toContain("Partial as of");
    expect(partial).toContain("· some code or scanners could not be fully analyzed");
  });

  it("stays present before any baseline exists", () => {
    expect(coverageSentence(null, NOW)).toBe("Coverage has not loaded.");
    expect(coverageSentence({ ...baseOverview, baseline: undefined }, NOW)).toBe("Incremental scanning not started · no full scan yet");
    expect(coverageSentence({ ...baseOverview, enabled: false }, NOW)).toBe("Automatic security scanning is off.");
  });

  it("does not infer complete coverage from a finished timestamp", () => {
    const overview = { ...baseOverview, last_full: { assessment_id: "a1", requested_at: "t", members: [], started_at: "2026-09-09T10:00:00Z", completed_at: "2026-09-09T10:30:00Z" } };
    expect(coverageSentence(overview, NOW)).toContain("Coverage unknown");
    expect(coverageChipState(overview)).toBe("unknown");
  });
});

describe("securityBaselineTime", () => {
  it("takes the later of the baseline and the last completed pass", () => {
    expect(securityBaselineTime(baseOverview)).toBe("2026-09-09T09:41:00Z");
    expect(securityBaselineTime({ ...baseOverview, last_full: { assessment_id: "a1", requested_at: "t", members: [], started_at: "2026-09-09T10:00:00Z", completed_at: "2026-09-09T10:30:00Z" } }))
      .toBe("2026-09-09T10:30:00Z");
    expect(securityBaselineTime({ ...baseOverview, last_full: { assessment_id: "a1", requested_at: "t", members: [], started_at: "2026-09-01T10:00:00Z", completed_at: "2026-09-01T10:30:00Z" } }))
      .toBe("2026-09-09T09:41:00Z");
  });
});

describe("filterFindingsNewSince", () => {
  it("keeps findings introduced at or after the baseline and drops those without history", () => {
    const rows = [
      { history: { introduced_at: "2026-09-09T09:41:00Z" } },
      { history: { introduced_at: "2026-09-09T09:40:59Z" } },
      { history: undefined },
    ];
    expect(filterFindingsNewSince(rows, "2026-09-09T09:41:00Z")).toEqual([rows[0]]);
    expect(filterFindingsNewSince(rows, undefined)).toHaveLength(3);
  });
});

describe("full scan projections", () => {
  it("checks every available scanner by default", () => {
    expect([...defaultFullScanSelection(baseOverview)]).toEqual(["sast"]);
  });

  it("reads chunk progress and the file total", () => {
    expect(fullScanProgressReadout(runningScan)).toBe("4 of 7 chunks · 12,702 files");
    expect(fullScanProgressReadout({ ...runningScan, progress: undefined })).toBe("Analyzing files");
    expect(fullScanScanStatus(runningScan)).toBe("Running");
    expect(fullScanScanStatus({ ...runningScan, status: "complete" })).toBe("Complete");
    expect(fullScanScanStatus({ ...runningScan, status: "failed" })).toBe("Failed");
    expect(fullScanScanStatus({ ...runningScan, status: "complete", coverage_status: "unavailable" })).toBe("Unavailable");
  });


  it("counts finished scanners into the coverage strip's control", () => {
    expect(fullScanToggleLabel(null)).toBe("Run full scan");
    const members: SecurityFullPassMember[] = [
      { scanner_id: "sast", phase: "started", scan: runningScan },
      { scanner_id: "sca", phase: "started", scan: { ...runningScan, id: "scan-2", status: "failed" } },
      { scanner_id: "secrets", phase: "not_started" },
      { scanner_id: "iac", phase: "waiting_for_pass" },
    ];
    const pass: SecurityFullPass = { assessment_id: "a3", requested_at: "t", members };
    expect(fullScanToggleLabel({ pass, live: true })).toBe("Full scan · 2 of 4 done");
    expect(fullScanToggleLabel({ pass, live: false })).toBe("Full scan finished");
  });

  it("says why a member that has not started is waiting", () => {
    const waitingForScanner: SecurityFullPassMember = { scanner_id: "sast", phase: "waiting_for_scanner" };
    const waitingForPass: SecurityFullPassMember = { scanner_id: "sca", phase: "waiting_for_pass" };
    const notStarted: SecurityFullPassMember = { scanner_id: "secrets", phase: "not_started" };
    expect([waitingForScanner, waitingForPass, notStarted].map(fullPassMemberStatus)).toEqual(["Waiting", "Waiting", "Not started"]);
    expect(fullPassMemberReadout(waitingForScanner)).toBe("Finishing an earlier scan first");
    expect(fullPassMemberReadout(waitingForPass)).toBe("Starts with the other scanners");
    expect(fullPassMemberReadout(waitingForPass, ["OpenGrep"])).toBe("Waiting for OpenGrep");
    expect(fullPassMemberReadout(waitingForPass, ["OpenGrep", "Gitleaks"])).toBe("Waiting for OpenGrep and Gitleaks");
    const held: SecurityFullPass = { assessment_id: "a3", requested_at: "t", members: [waitingForScanner, waitingForPass] };
    expect(fullPassWaitingOn(held, (id) => (id === "sast" ? "OpenGrep" : id))).toEqual(["OpenGrep"]);
    expect(fullPassMemberReadout(notStarted)).toBe("Left the pass before it started");
    expect(fullPassMemberMeasured(waitingForScanner)).toBe(false);
    const started: SecurityFullPassMember = { scanner_id: "sast", phase: "started", scan: runningScan };
    expect(fullPassMemberStatus(started)).toBe("Running");
    expect(fullPassMemberReadout(started)).toBe("4 of 7 chunks · 12,702 files");
    expect(fullPassMemberMeasured(started)).toBe(true);
  });

  it("reports coverage from the overview, then the last pass, then incremental", () => {
    expect(coverageChipState(baseOverview)).toBe("incremental");
    expect(coverageChipState({ ...baseOverview, last_full: { assessment_id: "a1", requested_at: "t", coverage_status: "bounded", members: [] } })).toBe("bounded");
    expect(coverageChipState({ ...baseOverview, coverage_status: "partial" })).toBe("partial");
  });
});
