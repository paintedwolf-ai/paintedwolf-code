import { describe, expect, it } from "vitest";
import { at } from "../test/at.ts";
import {
  advisoryKindLabel,
  formatAdvisory,
  formatLocation,
  formatScanEngine,
  formatScannerId,
  formatScanTimestamp,
  hintOverlaySnippet,
  kpiEntries,
  levelLabel,
  scanFindingsCounts,
  scanEmptyState,
  applyLiveScanPatch,
  liveScanWantsListRefresh,
  pickDefaultScanId,
  scansWantLiveRefresh,
} from "./scan-display.ts";
import type { CodeScan, SecurityFinding } from "../api/types.ts";

const finding: SecurityFinding = {
  rule_id: "opengrep:sql-concat",
  level: "high",
  message: "SQL built via string concat",
  locations: [{ uri: "src/db.go", start_line: 42 }],
  fingerprints: { primary: "fp-abc123" },
  tool: { driver_id: "opengrep", name: "OpenGrep" },
  properties: {
    lycaon: {
      hint_code: "SCAN_SQL_CONCAT",
      advisory: { osv_id: "OSV-1", cve_ids: ["CVE-2024-1"] },
    },
  },
};

describe("scan-display", () => {
  it("formats location with line", () => {
    expect(formatLocation(finding.locations[0])).toBe("src/db.go:42");
  });

  it("relativizes absolute locations under the repo root", () => {
    expect(
      formatLocation(
        { uri: "/Users/me/proj/src/db.go", start_line: 42 },
        "/Users/me/proj",
      ),
    ).toBe("src/db.go:42");
    expect(
      formatLocation(
        { uri: "file:///Users/me/proj/app.go", start_line: 1 },
        "/Users/me/proj",
      ),
    ).toBe("app.go:1");
    expect(
      formatLocation({ uri: "src/already.go", start_line: 3 }, "/Users/me/proj"),
    ).toBe("src/already.go:3");
  });

  it("formats advisory ids", () => {
    expect(formatAdvisory(finding.properties?.lycaon?.advisory)).toBe(
      "OSV-1 · CVE-2024-1",
    );
  });

  it("lists the canonical id once, then every alias family", () => {
    expect(
      formatAdvisory({
        osv_id: "CVE-2020-36567",
        cve_ids: ["CVE-2020-36567"],
        ghsa_ids: ["GHSA-6vm3-jj99-7229"],
        aliases: ["GO-2020-0001"],
      }),
    ).toBe("CVE-2020-36567 · GHSA-6vm3-jj99-7229 · GO-2020-0001");
  });

  it("labels malware reports and nothing else", () => {
    expect(advisoryKindLabel({ kind: "malicious_package" })).toBe("Malicious package");
    expect(advisoryKindLabel({ kind: "vulnerability" })).toBe("");
    expect(advisoryKindLabel(undefined)).toBe("");
  });

  it("builds severity KPI buckets in order (zero-filled)", () => {
    const rows = kpiEntries({ critical: 1, high: 2, info: 3, unknown: 4 });
    expect(rows.map((r) => r.label)).toEqual([
      "Critical",
      "High",
      "Medium",
      "Low",
      "Info",
      "Unknown",
    ]);
    expect(at(rows, 1).count).toBe(2);
    expect(at(rows, 2).count).toBe(0);
    expect(at(rows, 5).count).toBe(4);
  });

  it("renders scan findings counts", () => {
    const scan: CodeScan = {
      id: "s1",
      categories: ["sast"],
      status: "complete",
      findings_count: 10,
      long_running: false,
      findings_stored: 8,
      created_at: "t",
    };
    expect(scanFindingsCounts(scan)).toBe("8 of 10 findings");
  });

  it("emits a hint overlay snippet", () => {
    expect(hintOverlaySnippet(finding)).toContain("SCAN_SQL_CONCAT");
    expect(levelLabel("critical")).toBe("Critical");
  });

  it("formats scan run dashboard labels", () => {
    const scan: CodeScan = {
      id: "s1",
      categories: ["sast"],
      scanner_id: "opengrep-sast",
      status: "complete",
      findings_count: 3,
      long_running: false,
      findings_stored: 2,
      created_at: "2026-01-02T00:00:00Z",
      completed_at: "2026-01-02T01:00:00Z",
    };
		expect(formatScanEngine(scan)).toBe("opengrep-sast");
    const labels = {
      "lycaon-sast": "Static analysis",
      "lycaon-secrets": "Secrets",
      "lycaon-sca": "Supply chain",
    };
    expect(formatScannerId("lycaon-sast", labels)).toBe("Static analysis");
    expect(formatScannerId("lycaon-secrets", labels)).toBe("Secrets");
    expect(formatScannerId("lycaon-sca", labels)).toBe("Supply chain");
    expect(scanFindingsCounts(scan)).toBe("2 of 3 findings");
    expect(formatScanTimestamp(scan.completed_at)).not.toBe("—");
    expect(kpiEntries({ high: 1, medium: 1 }).map((r) => r.count)).toEqual([
      0, 1, 1, 0, 0, 0,
    ]);
  });

  it("detects when live refresh should stay active", () => {
    expect(scansWantLiveRefresh([], {
      scan_id: "s1",
      categories: ["sast"],
      status: "running",
      findings_count: 0,
      long_running: false,
    })).toBe(true);
    expect(scansWantLiveRefresh([{
      id: "s1",
      categories: ["sast"],
      status: "running",
      findings_count: 0,
      long_running: false,
      created_at: "t",
    }])).toBe(true);
    expect(scansWantLiveRefresh([{
      id: "s1",
      categories: ["sast"],
      status: "complete",
      findings_count: 1,
      long_running: false,
      created_at: "t",
    }])).toBe(false);
    expect(scansWantLiveRefresh([], {
      scan_id: "s1",
      categories: ["sast"],
      status: "canceled",
      findings_count: 0,
      long_running: false,
    })).toBe(false);
    expect(pickDefaultScanId(
      [
        { id: "older", categories: ["sast"], status: "complete", findings_count: 1, long_running: false, created_at: "t" },
        { id: "latest", categories: ["sast"], status: "running", findings_count: 0, long_running: false, created_at: "t" },
      ],
      "latest",
    )).toBe("latest");
  });

  it("patches findings_count without wanting list refresh", () => {
    const rows: CodeScan[] = [
      {
        id: "s1",
        categories: ["sast"],
        status: "running",
        findings_count: 0,
        long_running: false,
        created_at: "t",
      },
    ];
    const patched = applyLiveScanPatch(rows, {
      scan_id: "s1",
      categories: ["sast"],
      status: "running",
      findings_count: 7,
      long_running: false,
    });
    expect(patched.known).toBe(true);
    expect(patched.statusChanged).toBe(false);
    expect(at(patched.rows, 0).findings_count).toBe(7);
    expect(liveScanWantsListRefresh(true, false, "running")).toBe(false);
    expect(liveScanWantsListRefresh(true, true, "complete")).toBe(true);
    expect(liveScanWantsListRefresh(true, true, "canceled")).toBe(true);
  });

  it("patches execution policy without a status transition", () => {
    const rows: CodeScan[] = [{
      id: "s1",
      categories: ["sast"],
      status: "running",
      findings_count: 0,
      long_running: false,
      created_at: "2026-01-02T00:00:00Z",
    }];
    const patched = applyLiveScanPatch(rows, {
      scan_id: "s1",
      categories: ["sast"],
      status: "running",
      findings_count: 0,
      long_running: false,
      runtime: {
        soft_limit_ms: 900000,
        hard_limit_ms: 7200000,
        cpu_units: 2,
        parallelism: 2,
      },
      started_at: "2026-01-02T00:01:00Z",
    });
    expect(patched.rows).not.toBe(rows);
    expect(at(patched.rows, 0).started_at).toBe("2026-01-02T00:01:00Z");
    expect(at(patched.rows, 0).runtime?.hard_limit_ms).toBe(7200000);
    expect(patched.statusChanged).toBe(false);
  });
});

it("does not present pruned details as a clean run", () => {
  const state = scanEmptyState({ status: "complete", findings_count: 9, detail_pruned_at: "2026-09-11T00:00:00Z" } as CodeScan);
  expect(state.title).toBe("Scan details were pruned");
  expect(state.description).toContain("summary remains");
});
