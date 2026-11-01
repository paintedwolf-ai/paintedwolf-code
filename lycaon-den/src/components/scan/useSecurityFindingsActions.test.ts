import { createRoot, createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { CodeScan, FindingLedgerEntry, ProjectRoot, SecurityFinding } from "../../api/types.ts";
import type { FindingLedgerModel } from "./create-finding-ledger.ts";
import { useSecurityFindingsActions } from "./useSecurityFindingsActions.ts";

const addFindingsToChat = vi.hoisted(() => vi.fn());
const fixFindingsWithAgent = vi.hoisted(() => vi.fn());
const downloadExport = vi.hoisted(() => vi.fn(async (..._args: unknown[]) => undefined));

vi.mock("./add-findings-to-chat.ts", () => ({
  addFindingsToChat: (...args: unknown[]) => addFindingsToChat(...args),
}));

vi.mock("./fix-findings-with-agent.ts", () => ({
  fixFindingsWithAgent: (...args: unknown[]) => fixFindingsWithAgent(...args),
}));

vi.mock("../../platform/files/save-file.ts", () => ({
  downloadExport: (...args: unknown[]) => downloadExport(...args),
}));

const sampleFinding: SecurityFinding = {
  rule_id: "opengrep:sql",
  level: "high",
  message: "SQL injection",
  locations: [{ uri: "app.rb", start_line: 9 }],
  fingerprints: { primary: "fp-1" },
  tool: { driver_id: "opengrep", name: "OpenGrep" },
};

const sampleEntry = {
  finding: sampleFinding,
  state: "open",
  scanner_id: "opengrep",
  first_seen_at: "2026-01-01T00:00:00Z",
  last_seen_at: "2026-01-01T00:00:00Z",
  last_scan_id: "scan-1",
  observations: 1,
} as unknown as FindingLedgerEntry;

const sampleScan: CodeScan = {
  id: "scan-1",
  categories: ["sast"],
  status: "complete",
  findings_count: 1,
  long_running: false,
  created_at: "2026-01-01T00:00:00Z",
};

const sampleRoot: ProjectRoot = {
  id: "root-1",
  path: "/repo",
  label: "Repo",
  is_primary: true,
  added_at: "2026-01-01T00:00:00Z",
  kind: "attached",
};

describe("useSecurityFindingsActions", () => {
  beforeEach(() => {
    addFindingsToChat.mockReset();
    fixFindingsWithAgent.mockReset();
    downloadExport.mockClear();
  });

  it("exports ledger and run formats and triggers navigation callbacks", async () => {
    const [isLedger, setIsLedger] = createSignal(true);
    const errors: (string | null)[] = [];
    const onOpenCatalog = vi.fn();
    const onOpenRuns = vi.fn();
    const onBackToLedger = vi.fn();
    const clearSelection = vi.fn();

    const client = {
      exportCodeScanSARIF: vi.fn(async () => ({
        blob: new Blob(["sarif"]),
        filename: "scan-1.sarif",
      })),
      exportProjectFindings: vi.fn(async () => ({
        blob: new Blob(["ledger"]),
        filename: "ledger.sarif",
      })),
    } as unknown as LycaonClient;

    const ledger = {
      totalMatch: () => 1,
      entries: () => [sampleEntry],
      exportQuery: () => ({ status: ["open" as const] }),
      clearSelection,
    } as unknown as FindingLedgerModel;

    await createRoot(async (dispose) => {
      const actions = useSecurityFindingsActions({
        client: () => client,
        projectId: () => "proj-1",
        rootId: () => "root-1",
        repoRoot: () => "/repo",
        roots: () => [sampleRoot],
        isLedger,
        ledger,
        selectedScan: () => sampleScan,
        scanResultsValue: () => ({ truncated: true }),
        scanResultsLoading: () => false,
        allFindings: () => [sampleFinding],
        runFilteredFindings: () => [sampleFinding],
        runFiltersActive: () => true,
        setError: (err) => errors.push(err),
        onOpenCatalog,
        onOpenRuns,
        onBackToLedger,
      });

      for (const item of actions.overflowItems()) {
        if ("onSelect" in item) item.onSelect?.();
      }
      await Promise.resolve();
      expect(client.exportProjectFindings).toHaveBeenCalledTimes(2);
      expect(onOpenCatalog).toHaveBeenCalledOnce();
      expect(onOpenRuns).toHaveBeenCalledOnce();

      setIsLedger(false);
      for (const item of actions.overflowItems()) {
        if ("onSelect" in item) item.onSelect?.();
      }
      await Promise.resolve();
      expect(onBackToLedger).toHaveBeenCalledOnce();
      expect(client.exportCodeScanSARIF).toHaveBeenCalledWith("proj-1", "scan-1");
      expect(downloadExport).toHaveBeenCalled();
      expect(errors.some((e) => e?.includes("loaded findings"))).toBe(true);

      addFindingsToChat.mockResolvedValueOnce({ ok: false, reason: "chat busy" });
      await actions.attachFindings([{ finding: sampleFinding, last_scan_id: "scan-1" }]);
      expect(errors).toContain("chat busy");

      addFindingsToChat.mockRejectedValueOnce(new Error("chat failed"));
      await actions.attachFindings([{ finding: sampleFinding, last_scan_id: "scan-1" }]);
      expect(errors.some((e) => e?.includes("chat failed"))).toBe(true);

      fixFindingsWithAgent.mockResolvedValueOnce({ ok: true });
      await actions.handOffToAgent([sampleEntry]);
      expect(clearSelection).toHaveBeenCalledOnce();

      fixFindingsWithAgent.mockResolvedValueOnce({ ok: false, reason: "agent busy" });
      await actions.handOffToAgent([sampleEntry]);
      expect(errors).toContain("agent busy");

      dispose();
    });
  });
});
