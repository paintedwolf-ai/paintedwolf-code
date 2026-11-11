import { batch, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  CodeScan,
  FindingLedgerEntry,
  ProjectRoot,
  SecurityFinding,
} from "../../api/types.ts";
import { contextAction } from "../context-actions.ts";
import type { ContextMenuItem } from "../ContextMenu.tsx";
import {
  buildFindingsExportBlob,
  findingsExportFilename,
  type ScanExportFormat,
} from "../../lib/scan-export.ts";
import { FINDINGS_QUERY_LIMIT } from "../../lib/scan-findings-table.ts";
import { downloadExport } from "../../platform/files/save-file.ts";
import { fixFindingsWithAgent } from "./fix-findings-with-agent.ts";
import { addFindingsToChat, type FindingChatContext } from "./add-findings-to-chat.ts";
import type { FindingLedgerModel } from "./create-finding-ledger.ts";

export type UseSecurityFindingsActionsOptions = {
  client: () => LycaonClient | undefined;
  projectId: () => string | undefined;
  rootId: () => string | undefined;
  repoRoot: () => string | undefined;
  roots: () => readonly ProjectRoot[] | undefined;
  isLedger: () => boolean;
  ledger: FindingLedgerModel;
  selectedScan: () => CodeScan | null;
  scanResultsValue: () => { truncated?: boolean } | null | undefined;
  scanResultsLoading: () => boolean;
  allFindings: () => SecurityFinding[];
  runFilteredFindings: () => SecurityFinding[];
  runFiltersActive: () => boolean;
  setError: (error: string | null) => void;
  onOpenCatalog: () => void;
  onOpenRuns: () => void;
  onBackToLedger: () => void;
};

export function useSecurityFindingsActions(options: UseSecurityFindingsActionsOptions) {
  const [addingToChat, setAddingToChat] = createSignal(false);

  const exportDisabled = () =>
    options.selectedScan()?.status !== "complete" ||
    options.scanResultsValue() == null ||
    options.scanResultsLoading();

  const exportSarif = async () => {
    const c = options.client();
    const id = options.selectedScan()?.id;
    const projectId = options.projectId();
    if (!c || !id || !projectId) return;
    const { blob, filename } = await c.exportCodeScanSARIF(projectId, id);
    await downloadExport(blob, filename);
  };

  const exportFindings = async (format: ScanExportFormat, scope: "run" | "visible") => {
    const id = options.selectedScan()?.id;
    if (!id) return;
    if (options.scanResultsValue()?.truncated === true) {
      options.setError(
        `Export uses the first ${FINDINGS_QUERY_LIMIT} loaded findings; some rows may be missing.`,
      );
    }
    const rows =
      scope === "visible" ? options.runFilteredFindings() : options.allFindings();
    const blob = buildFindingsExportBlob(rows, format, options.repoRoot());
    await downloadExport(blob, findingsExportFilename(id, format, scope));
  };

  /** Exports all matching findings using the current filters. */
  const exportLedger = async (format: "sarif" | "openvex") => {
    const c = options.client();
    const projectId = options.projectId()?.trim();
    if (!c || !projectId) return;
    const { blob, filename } = await c.exportProjectFindings(projectId, {
      format,
      query: options.ledger.exportQuery(),
    });
    await downloadExport(blob, filename);
  };

  const exportLedgerRows = async (format: ScanExportFormat) => {
    const projectId = options.projectId()?.trim();
    if (!projectId) return;
    const rows = options.ledger.entries().map((entry) => entry.finding);
    const blob = buildFindingsExportBlob(rows, format, options.repoRoot());
    await downloadExport(blob, findingsExportFilename(projectId, format, "visible"));
  };

  const attachFindings = async (entries: readonly FindingChatContext[]) => {
    const projectId = options.projectId()?.trim();
    if (!projectId || addingToChat()) return;
    setAddingToChat(true);
    options.setError(null);
    try {
      const rootId = options.rootId();
      const selectedRoot = options.roots()?.find((root) => root.id === rootId);
      const result = selectedRoot
        ? await addFindingsToChat(projectId, entries, selectedRoot)
        : await addFindingsToChat(projectId, entries);
      if (options.projectId()?.trim() !== projectId) return;
      if (!result.ok) options.setError(result.reason);
    } catch (error) {
      if (options.projectId()?.trim() === projectId) options.setError(String(error));
    } finally {
      setAddingToChat(false);
    }
  };

  /** Stages a draft for the user to review and send. */
  const handOffToAgent = async (entries: readonly FindingLedgerEntry[]) => {
    const projectId = options.projectId()?.trim();
    if (!projectId) return;
    const rootId = options.rootId();
    const allRoots = options.roots() ?? [];
    const result = await fixFindingsWithAgent({
      projectId,
      roots: rootId ? allRoots.filter((root) => root.id === rootId) : allRoots,
      entries,
    });
    if (!result.ok) options.setError(result.reason);
    else options.ledger.clearSelection();
  };

  /** SARIF and OpenVEX include all matches; CSV includes the loaded page. */
  const overflowItems = (): ContextMenuItem[] => {
    if (options.isLedger()) {
      const empty = () => options.ledger.totalMatch() === 0;
      return [
        contextAction("exportSarif", {
          testId: "ledger-export-sarif",
          disabled: empty(),
          onSelect: () =>
            void exportLedger("sarif").catch((err) => options.setError(String(err))),
        }),
        contextAction("exportOpenVex", {
          testId: "ledger-export-openvex",
          disabled: empty(),
          onSelect: () =>
            void exportLedger("openvex").catch((err) => options.setError(String(err))),
        }),
        contextAction("exportCsvPage", {
          testId: "ledger-export-csv",
          disabled: options.ledger.entries().length === 0,
          onSelect: () =>
            void exportLedgerRows("csv").catch((err) => options.setError(String(err))),
        }),
        contextAction("ignoreRules", {
          testId: "ledger-open-catalog",
          onSelect: () => batch(options.onOpenCatalog),
        }),
        contextAction("scanRuns", {
          testId: "ledger-open-runs",
          onSelect: () => batch(options.onOpenRuns),
        }),
      ];
    }
    const items: ContextMenuItem[] = [
      contextAction("backToFindings", {
        testId: "scans-back-to-ledger",
        onSelect: () => batch(options.onBackToLedger),
      }),
      contextAction("exportSarif", {
        testId: "scans-export-run-sarif",
        disabled: exportDisabled(),
        onSelect: () => void exportSarif().catch((err) => options.setError(String(err))),
      }),
      contextAction("exportJsonl", {
        testId: "scans-export-run-jsonl",
        disabled: exportDisabled(),
        onSelect: () =>
          void exportFindings("jsonl", "run").catch((err) => options.setError(String(err))),
      }),
      contextAction("exportCsv", {
        testId: "scans-export-run-csv",
        disabled: exportDisabled(),
        onSelect: () =>
          void exportFindings("csv", "run").catch((err) => options.setError(String(err))),
      }),
    ];
    if (options.runFiltersActive()) {
      items.push(
        contextAction("exportFilteredCsv", {
          testId: "scans-export-visible-csv",
          disabled: exportDisabled(),
          onSelect: () =>
            void exportFindings("csv", "visible").catch((err) =>
              options.setError(String(err)),
            ),
        }),
      );
    }
    return items;
  };

  return {
    addingToChat,
    attachFindings,
    handOffToAgent,
    overflowItems,
  };
}
