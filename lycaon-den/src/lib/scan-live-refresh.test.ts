import { describe, expect, it } from "vitest";
import { at } from "../test/at.ts";
import type { CodeScan } from "../api/types.ts";
import { createLiveScanListGate } from "./scan-live-refresh.ts";
import { SCANS_LIVE_REFRESH_MS } from "./scan-display.ts";

describe("createLiveScanListGate", () => {
  it("does not list on findings_count ticks once the row is known", () => {
    const gate = createLiveScanListGate();
    let history: CodeScan[] = [
      {
        id: "scan-1",
        categories: ["sast"],
        status: "running",
        findings_count: 0,
        long_running: false,
        created_at: "t",
      },
    ];
    let listCalls = 0;
    for (let n = 1; n <= 12; n++) {
      const d = gate.decide(
        {
          scan_id: "scan-1",
          categories: ["sast"],
          status: "running",
          findings_count: n,
          long_running: false,
        },
        history,
      );
      history = d.rows;
      if (d.shouldList) listCalls++;
    }
    expect(listCalls).toBe(0);
    expect(at(history, 0).findings_count).toBe(12);
  });

  it("lists once on terminal status transition", () => {
    const gate = createLiveScanListGate();
    const history: CodeScan[] = [
      {
        id: "scan-1",
        categories: ["sast"],
        status: "running",
        findings_count: 3,
        long_running: false,
        created_at: "t",
      },
    ];
    const d = gate.decide(
      {
        scan_id: "scan-1",
        categories: ["sast"],
        status: "complete",
        findings_count: 3,
        long_running: false,
      },
      history,
    );
    expect(d.shouldList).toBe(true);
    expect(d.statusChanged).toBe(true);
  });

  it("coalesces unknown-scan discover to one list", () => {
    const gate = createLiveScanListGate();
    let listCalls = 0;
    for (let n = 0; n < 5; n++) {
      const d = gate.decide(
        {
          scan_id: "scan-new",
          categories: ["sast"],
          status: "running",
          findings_count: n,
          long_running: false,
        },
        [],
      );
      if (d.shouldList) listCalls++;
    }
    expect(listCalls).toBe(1);
  });

  it("rediscovers an unknown scan when it finishes and resets for another project", () => {
    const gate = createLiveScanListGate();
    const event = { scan_id: "off-page", categories: ["sast"] as CodeScan["categories"], status: "running" as const,
      findings_count: 0, long_running: false };
    expect(gate.decide(event, []).shouldList).toBe(true);
    expect(gate.decide(event, []).shouldList).toBe(false);
    const done = { ...event, status: "complete" as const };
    expect(gate.decide(done, []).shouldList).toBe(true);
    expect(gate.decide(done, []).shouldList).toBe(false);
    gate.reset();
    expect(gate.decide(done, []).shouldList).toBe(true);
  });

  it("preserves progress, coverage, and error changes without a status transition", () => {
    const gate = createLiveScanListGate();
    const row: CodeScan = { id: "scan", categories: ["sast"], status: "running", findings_count: 0,
      long_running: false, created_at: "t" };
    const decision = gate.decide({ scan_id: row.id, categories: row.categories, status: row.status,
      findings_count: 0, long_running: false, progress: { chunks: 3, completed: 1, files: 20 },
      coverage_status: "partial", error: "Recorded detail" }, [row]);
    expect(decision.rows[0]).toMatchObject({ progress: { completed: 1 }, coverage_status: "partial", error: "Recorded detail" });
    expect(decision.shouldList).toBe(false);
  });

  it("exports LIVE_REFRESH floor ≥ 3000ms", () => {
    expect(SCANS_LIVE_REFRESH_MS).toBeGreaterThanOrEqual(3000);
  });
});
