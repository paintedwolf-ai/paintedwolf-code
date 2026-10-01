import { contextAction } from "../context-actions.ts";
import {
  For,
  Show,
  batch,
  createComputed,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  untrack,
  type JSX,
} from "solid-js";
import type {
  CodeScanEvent,
  FindingLedgerEntry,
  FindingLedgerState,
  FindingLevel,
  ProjectRoot,
  SecurityFinding,
  SecurityOverview,
} from "../../api/types.ts";
import { createScanDetail } from "./create-scan-detail.ts";
import { createFindingLedger } from "./create-finding-ledger.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { ScanLimitations } from "./ScanLimitations.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { LedgerFindingDetail } from "./LedgerFindingDetail.tsx";
import { LedgerFindingsList } from "./LedgerFindingsList.tsx";
import { RunFindingDetail } from "./RunFindingDetail.tsx";
import { RunFindingsList } from "./RunFindingsList.tsx";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import {
  buildFindingsExportBlob,
  findingsExportFilename,
  type ScanExportFormat,
} from "../../lib/scan-export.ts";
import { FindingFilterChip } from "./FindingFilterChip.tsx";
import { IgnoreCatalogPanel } from "./IgnoreCatalogPanel.tsx";
import { IgnoreFindingPanel } from "./IgnoreFindingPanel.tsx";
import { fixFindingsWithAgent } from "./fix-findings-with-agent.ts";
import { addFindingsToChat, type FindingChatContext } from "./add-findings-to-chat.ts";
import {
  FINDINGS_PAGE_SIZE,
  FINDINGS_QUERY_LIMIT,
  HISTORY_PAGE_SIZE,
  type RunSortKey,
  filterFindingsByLevels,
  nextRunSortDirection,
} from "../../lib/scan-findings-table.ts";
import { LEDGER_LIST_SURFACE, SECURITY_LIST_SURFACE } from "../../lib/scan-columns.ts";
import type { SortDirection, SortState } from "../../list/list-sort.ts";
import { commitListSort, listPanePrefs } from "../../shell/layout-store.ts";
import { resolveListPaneSort } from "../../list/list-pane-model.ts";
import {
  kpiEntries,
  pickDefaultScanId,
  scanFindingsCounts,
  SCANS_LIVE_REFRESH_MS,
  SCANS_LIVE_REFRESH_MAX_MS,
  scansWantLiveRefresh,
} from "../../lib/scan-display.ts";
import {
  anyPassSuperseded,
  ledgerAllCount,
  ledgerCountsSummary,
  ledgerEntryKey,
  ledgerOpenCount,
} from "../../lib/ledger-display.ts";
import { createLiveScanListGate } from "../../lib/scan-live-refresh.ts";
import {
  defaultFullScanSelection,
  filterFindingsNewSince,
  securityBaselineTime,
} from "../../lib/scan-coverage.ts";
import { downloadExport } from "../../platform/files/save-file.ts";
import {
  onFindingFocusRequest,
  takeFindingFocusRequest,
} from "../../platform/navigation/open-finding.ts";
import { useSettingsBackend } from "../../settings/settings-backend.ts";
import { SECURITY_SCANNERS_SECTION_LABEL } from "../../settings/settings-nav-model.ts";
import { scannerJobLabel } from "../../settings/extensions/scanners-catalog-model.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";
import type { ContextMenuItem } from "../ContextMenu.tsx";
import { BrowseChip } from "../browse/BrowseChip.tsx";
import { BrowseSegmented } from "../browse/BrowseSegmented.tsx";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { ScanRunPicker } from "./ScanRunPicker.tsx";
import { SecurityCoverage } from "./SecurityCoverage.tsx";
import { SecurityEmptyState, emptyReasonFor } from "./SecurityEmptyState.tsx";
import { createDisplayedFullPass } from "./displayed-full-pass.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { createAdaptivePoller } from "../../store/adaptive-poller.ts";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { selectedSecurityFolder, selectSecurityFolder } from "../../platform/files/security-folder-selection.ts";
import { rootScanClient } from "./root-scan-client.ts";
import { createProjectSecurityQuery, securityOverviewRunning } from "./project-security-query.ts";

type Props = {
  appStore: AppStore;
  projectId: string | undefined;
  /** Base for repository-relative finding paths. */
  repoRoot?: string;
  /** Attached roots used to locate finding files. */
  roots?: readonly ProjectRoot[];
  latestScanId?: string;
  liveScan?: CodeScanEvent;
  back?: StageBack | null;
};

type StageView = "ledger" | "runs";

const OPEN_TAB_STATES: FindingLedgerState[] = ["open", "reopened"];
const ALL_TAB_STATES: FindingLedgerState[] = [
  "open",
  "reopened",
  "ignored",
  "fixed",
  "unverified",
  "not_observed",
];

export function SecurityFindingsPane(props: Props) {
  const root = createMemo(() => props.roots?.find((entry) => entry.id === selectedSecurityFolder(props.projectId))
    ?? props.roots?.find((entry) => entry.is_primary)
    ?? props.roots?.[0]);
  const identity = () => JSON.stringify([props.projectId, root()?.id]);
  return <Show when={identity()} keyed>{(_identity) => (
    <FolderSecurityFindingsPane
      {...props}
      rootId={root()?.id}
      repoRoot={root()?.path ?? props.repoRoot}
      folderPicker={<Show when={(props.roots?.length ?? 0) > 1}>
        <DenSelect
          aria-label="Security folder"
          value={root()?.id ?? ""}
          options={(props.roots ?? []).map((entry) => ({ value: entry.id, label: entry.label }))}
          onValueChange={(id) => { if (props.projectId) selectSecurityFolder(props.projectId, id); }}
        />
      </Show>}
    />
  )}</Show>;
}

function FolderSecurityFindingsPane(props: Props & { rootId?: string; folderPicker?: JSX.Element }) {
  const { client: settingsClient } = useSettingsBackend(props.appStore);
  const client = createMemo(() => {
    const current = settingsClient();
    return current && props.rootId ? rootScanClient(current, props.rootId) : current ?? undefined;
  });
  const residentLive = useResidentLive();

  const [view, setView] = createSignal<StageView>("ledger");
  const [selectedId, setSelectedId] = createSignal<string | null>(null);
  const [ledgerKey, setLedgerKey] = createSignal<string | null>(null);
  const [runSortKey, setRunSortKey] = createSignal<RunSortKey>("date");
  const [runSortDir, setRunSortDir] = createSignal<SortDirection>("desc");
  const [historyPage, setHistoryPage] = createSignal(0);
  const [historyCursors, setHistoryCursors] = createSignal<string[]>([""]);
  const [startingFullScan, setStartingFullScan] = createSignal(false);
  const [drillId, setDrillId] = createSignal<string | null>(null);
  const [actionError, setError] = createSignal<string | null>(null);
  const [findingsPage, setFindingsPage] = createSignal(0);
  const [newSince, setNewSince] = createSignal(false);
  const [ignoreOpen, setIgnoreOpen] = createSignal(false);
  const [catalogOpen, setCatalogOpen] = createSignal(false);
  let ignoreAnchorEl: HTMLButtonElement | undefined;

  const resetHistory = () =>
    batch(() => {
      setHistoryPage(0);
      setHistoryCursors([""]);
    });

  const catalogQuery = createSurfaceQuery({
    name: "scan-catalog",
    source: () => {
      const c = client();
      const projectId = props.projectId?.trim();
      return c && projectId ? { client: c, projectId, key: projectId } : null;
    },
    load: async ({ client, projectId }) => {
      const catalog = await client.listProjectScanners(projectId);
      return Object.fromEntries(
        catalog.scanners.map((scanner) => [scanner.id, scannerJobLabel(scanner)]),
      );
    },
    required: false,
  });

  const historyQuery = createSurfaceQuery({
    name: "scan-history",
    source: () => {
      const c = client();
      const projectId = props.projectId?.trim();
      const sort = runSortKey(),
        order = runSortDir(),
        cursor = historyCursors()[historyPage()] ?? "";
      return c && projectId
        ? { client: c, projectId, sort, order, cursor, key: JSON.stringify([projectId, sort, order, cursor]) }
        : null;
    },
    scope: ({ projectId }) => projectId,
    load: async ({ client, projectId, sort, order, cursor }) =>
      client.listCodeScans(projectId, { limit: HISTORY_PAGE_SIZE, cursor, sort, order }),
    required: false,
  });

  // The shared client keys the record a chat's phase note also reads.
  const securityQuery = createProjectSecurityQuery(() => {
    const c = settingsClient();
    const projectId = props.projectId?.trim();
    return c && projectId ? { client: c, projectId, rootId: props.rootId } : null;
  });

  const ledger = createFindingLedger({
    client,
    projectId: () => props.projectId?.trim(),
  });

  const history = () => historyQuery.value()?.scans ?? [];
  const shownHistoryPage = () => {
    const shown = historyQuery.displayed()?.source.cursor;
    const index = shown === undefined ? -1 : historyCursors().indexOf(shown);
    return index >= 0 ? index : historyPage();
  };
  const scannerLabels = () => catalogQuery.value() ?? {};
  const overview = (): SecurityOverview | null => securityQuery.value() ?? null;
  const displayedPass = createDisplayedFullPass(overview, () => props.projectId?.trim());
  const baselineTime = () => securityBaselineTime(overview());

  const detail = createScanDetail({
    client,
    projectId: () => props.projectId?.trim(),
    scanId: selectedId,
    liveScan: () => props.liveScan,
    fixedSince: () => (newSince() ? baselineTime() : undefined),
  });

  const selectedScan = detail.scan;
  const allFindings = detail.findings;
  const isLedger = () => view() === "ledger";

  let listScrollEl: HTMLDivElement | undefined;
  const resetListScroll = () => {
    if (!listScrollEl) return;
    const motion = scrollportMotionForViewport(listScrollEl);
    if (!motion) return;
    motion.cancelApplicationMotion();
    motion.commit(0, "jump");
  };

  // Reset scroll only when displayed result set changes.
  createEffect(() => {
    if (isLedger()) {
      ledger.shownKey();
    } else {
      findingsPage();
    }
    untrack(resetListScroll);
  });

  const restartRunView = () => {
    setFindingsPage(0);
    resetListScroll();
  };

  const [severityFilter, setSeverityFilter] = createSignal(new Set() as Set<FindingLevel>);
  const liveListGate = createLiveScanListGate();

  const runSort = (): SortState => resolveListPaneSort(listPanePrefs(SECURITY_LIST_SURFACE));
  const ledgerPaneSurface = () => (isLedger() ? LEDGER_LIST_SURFACE : SECURITY_LIST_SURFACE);

  const runFilteredFindings = createMemo(() => {
    const byLevel = filterFindingsByLevels(allFindings(), severityFilter());
    return newSince() ? filterFindingsNewSince(byLevel, baselineTime()) : byLevel;
  });
  const findingsWithoutHistory = () =>
    allFindings().filter((finding) => !finding.history?.introduced_at).length;
  const severityFilterActive = createMemo(() => severityFilter().size > 0);
  const runFiltersActive = createMemo(() => severityFilterActive() || newSince());

  const selectedRunFinding = (): SecurityFinding | null => {
    const id = drillId();
    if (!id) return null;
    return allFindings().find((finding) => finding.fingerprints.primary === id) ?? null;
  };
  const selectedLedgerEntry = (): FindingLedgerEntry | null => {
    const key = ledgerKey();
    if (!key) return null;
    return ledger.entries().find((entry) => ledgerEntryKey(entry) === key) ?? null;
  };

  const clearRunSelection = () =>
    batch(() => {
      setError(null);
      setStartingFullScan(false);
      restartRunView();
      setSeverityFilter(new Set() as Set<FindingLevel>);
      setDrillId(null);
      setSelectedId(null);
    });

  const selectScan = (scanId: string, options?: { restart?: boolean }) => {
    if (!options?.restart && scanId === selectedId()) return;
    batch(() => {
      setError(null);
      setDrillId(null);
      restartRunView();
      setSeverityFilter(new Set() as Set<FindingLevel>);
      setSelectedId(scanId);
    });
  };

  const openRun = (scanId: string) => {
    batch(() => {
      setView("runs");
      setLedgerKey(null);
      selectScan(scanId, { restart: true });
    });
  };

  const toggleRunSeverity = (level: FindingLevel) => {
    setSeverityFilter((prev) => {
      const next = new Set(prev);
      if (next.has(level)) next.delete(level);
      else next.add(level);
      return next;
    });
    restartRunView();
  };

  const clearFilters = () => {
    if (isLedger()) {
      ledger.clearFilters();
      return;
    }
    batch(() => {
      setSeverityFilter(new Set() as Set<FindingLevel>);
      setNewSince(false);
      restartRunView();
    });
  };

  const refreshScans = async () => {
    await historyQuery.refresh();
  };

  let previousProject: string | undefined;
  createComputed(() => {
    const projectId = props.projectId;
    if (projectId === previousProject) return;
    previousProject = projectId;
    liveListGate.reset();
    untrack(() => {
      clearRunSelection();
      resetHistory();
      setNewSince(false);
      setLedgerKey(null);
      ledger.clearFilters();
    });
  });

  createEffect(() => {
    const page = historyQuery.value();
    if (!page) return;
    untrack(() =>
      batch(() => {
        if (selectedId()) return;
        const nextId = pickDefaultScanId(page.scans, props.latestScanId);
        if (nextId) selectScan(nextId);
        else clearRunSelection();
      }),
    );
  });

  createEffect(() => {
    const value = detail.results.value();
    if (!value) return;
    untrack(() => {
      const pages = Math.ceil((value.findings?.length ?? 0) / FINDINGS_PAGE_SIZE);
      const page = Math.min(findingsPage(), Math.max(0, pages - 1));
      if (page !== findingsPage()) setFindingsPage(page);
    });
  });

  /** Finding focus resolves across scanners through the project ledger. */
  const applyFindingFocus = async () => {
    const request = takeFindingFocusRequest();
    if (!request) return;
    const projectId = untrack(() => props.projectId?.trim());
    if (!projectId || request.projectId !== projectId) return;
    if (request.scanId) {
      openRun(request.scanId);
      setDrillId(request.fingerprint);
      await detail.refresh();
      setDrillId(request.fingerprint);
      return;
    }
    batch(() => {
      setView("ledger");
      ledger.clearFilters();
      ledger.setFingerprint(request.fingerprint);
    });
    await ledger.query.refresh();
    const match = untrack(() => ledger.entries())[0];
    if (match) setLedgerKey(ledgerEntryKey(match));
  };

  createEffect(() => {
    props.projectId;
    client();
    if (!residentLive()) return;
    const unsubscribe = onFindingFocusRequest(() => {
      void applyFindingFocus();
    });
    void applyFindingFocus();
    onCleanup(unsubscribe);
  });

  createEffect(() => {
    const live = props.liveScan;
    const active = residentLive();
    if (!live?.scan_id) return;
    untrack(() => {
      const decision = liveListGate.decide(live, history());
      const previous = historyQuery.value();
      if (previous && decision.rows !== previous.scans) {
        historyQuery.publish({ ...previous, scans: decision.rows });
      }
      if (active && decision.shouldList) void refreshScans();
      if (active) {
        void securityQuery.refresh();
        void ledger.query.refresh();
      }
    });
  });

  const livePoll = createAdaptivePoller(
    async () => {
      await Promise.all([refreshScans(), securityQuery.refresh()]);
    },
    SCANS_LIVE_REFRESH_MS,
    SCANS_LIVE_REFRESH_MAX_MS,
  );
  onCleanup(() => livePoll.cancel());

  createEffect(() => {
    const c = client();
    const projectId = props.projectId?.trim();
    const wantsRefresh =
      scansWantLiveRefresh(history(), props.liveScan, selectedScan()) ||
      securityOverviewRunning(overview());
    livePoll.setEnabled(Boolean(c && projectId && residentLive() && wantsRefresh));
  });

  const startFullScan = async (scannerIds: string[]) => {
    const c = client();
    const projectId = props.projectId?.trim();
    if (!c || !projectId) return;
    const target = securityQuery.capture();
    setStartingFullScan(true);
    setError(null);
    try {
      const started = await c.startFullScan(projectId, { kind: "full", scanner_ids: scannerIds });
      if (!target.current()) return;
      // Publish immediately so pass displays before next read.
      target.publish(started.overview);
      await Promise.all([refreshScans(), ledger.query.refresh()]);
    } catch (err) {
      if (target.current()) setError(String(err));
    } finally {
      if (target.current()) setStartingFullScan(false);
    }
  };

  const exportDisabled = () =>
    selectedScan()?.status !== "complete" ||
    detail.results.value() == null ||
    detail.results.loading();

  const exportSarif = async () => {
    const c = client();
    const id = selectedScan()?.id;
    if (!c || !id || !props.projectId) return;
    const { blob, filename } = await c.exportCodeScanSARIF(props.projectId, id);
    await downloadExport(blob, filename);
  };

  const exportFindings = async (format: ScanExportFormat, scope: "run" | "visible") => {
    const id = selectedScan()?.id;
    if (!id) return;
    if (detail.results.value()?.truncated === true) {
      setError(
        `Export uses the first ${FINDINGS_QUERY_LIMIT} loaded findings; some rows may be missing.`,
      );
    }
    const rows = scope === "visible" ? runFilteredFindings() : allFindings();
    const blob = buildFindingsExportBlob(rows, format, props.repoRoot);
    await downloadExport(blob, findingsExportFilename(id, format, scope));
  };

  /** Exports all matching findings using the current filters. */
  const exportLedger = async (format: "sarif" | "openvex") => {
    const c = client();
    const projectId = props.projectId?.trim();
    if (!c || !projectId) return;
    const { blob, filename } = await c.exportProjectFindings(projectId, {
      format,
      query: ledger.exportQuery(),
    });
    await downloadExport(blob, filename);
  };

  const [addingToChat, setAddingToChat] = createSignal(false);
  const attachFindings = async (entries: readonly FindingChatContext[]) => {
    const projectId = props.projectId?.trim();
    if (!projectId || addingToChat()) return;
    setAddingToChat(true);
    setError(null);
    try {
      const selectedRoot = props.roots?.find((root) => root.id === props.rootId);
      const result = selectedRoot
        ? await addFindingsToChat(projectId, entries, selectedRoot)
        : await addFindingsToChat(projectId, entries);
      if (props.projectId?.trim() !== projectId) return;
      if (!result.ok) setError(result.reason);
    } catch (error) {
      if (props.projectId?.trim() === projectId) setError(String(error));
    } finally {
      setAddingToChat(false);
    }
  };

  /** Stages a draft for the user to review and send. */
  const handOffToAgent = async (entries: readonly FindingLedgerEntry[]) => {
    const projectId = props.projectId?.trim();
    if (!projectId) return;
    const result = await fixFindingsWithAgent({
      projectId,
      roots: props.rootId ? (props.roots ?? []).filter((root) => root.id === props.rootId) : props.roots ?? [],
      entries,
    });
    if (!result.ok) setError(result.reason);
    else ledger.clearSelection();
  };

  const exportLedgerRows = async (format: ScanExportFormat) => {
    const projectId = props.projectId?.trim();
    if (!projectId) return;
    const rows = ledger.entries().map((entry) => entry.finding);
    const blob = buildFindingsExportBlob(rows, format, props.repoRoot);
    await downloadExport(blob, findingsExportFilename(projectId, format, "visible"));
  };

  /** SARIF and OpenVEX include all matches; CSV includes the loaded page. */
  const overflowItems = (): ContextMenuItem[] => {
    if (isLedger()) {
      const empty = () => ledger.totalMatch() === 0;
      return [
        contextAction("exportSarif", {
          testId: "ledger-export-sarif",
          disabled: empty(),
          onSelect: () => void exportLedger("sarif").catch((err) => setError(String(err))),
        }),
        contextAction("exportOpenVex", {
          testId: "ledger-export-openvex",
          disabled: empty(),
          onSelect: () => void exportLedger("openvex").catch((err) => setError(String(err))),
        }),
        contextAction("exportCsvPage", {
          testId: "ledger-export-csv",
          disabled: ledger.entries().length === 0,
          onSelect: () => void exportLedgerRows("csv").catch((err) => setError(String(err))),
        }),
        contextAction("ignoreRules", {
          testId: "ledger-open-catalog",
          onSelect: () =>
            batch(() => {
              setCatalogOpen(true);
              setLedgerKey(null);
              ledger.clearSelection();
            }),
        }),
        contextAction("scanRuns", {
          testId: "ledger-open-runs",
          onSelect: () =>
            batch(() => {
              setView("runs");
              setLedgerKey(null);
            }),
        }),
      ];
    }
    const items: ContextMenuItem[] = [
      contextAction("backToFindings", {
        testId: "scans-back-to-ledger",
        onSelect: () =>
          batch(() => {
            setView("ledger");
            setDrillId(null);
          }),
      }),
      contextAction("exportSarif", {
        testId: "scans-export-run-sarif",
        disabled: exportDisabled(),
        onSelect: () => void exportSarif().catch((err) => setError(String(err))),
      }),
      contextAction("exportJsonl", {
        testId: "scans-export-run-jsonl",
        disabled: exportDisabled(),
        onSelect: () => void exportFindings("jsonl", "run").catch((err) => setError(String(err))),
      }),
      contextAction("exportCsv", {
        testId: "scans-export-run-csv",
        disabled: exportDisabled(),
        onSelect: () => void exportFindings("csv", "run").catch((err) => setError(String(err))),
      }),
    ];
    if (runFiltersActive()) {
      items.push(contextAction("exportFilteredCsv", {
        testId: "scans-export-visible-csv",
        disabled: exportDisabled(),
        onSelect: () =>
          void exportFindings("csv", "visible").catch((err) => setError(String(err))),
      }));
    }
    return items;
  };

  const loadError = () =>
    historyQuery.error() ??
    catalogQuery.error() ??
    securityQuery.error() ??
    (isLedger()
      ? ledger.query.error()
      : (detail.summary.error() ?? detail.results.error()));
  const error = () => actionError() ?? loadError();
  const retryLoading = async () => {
    await Promise.all([
      historyQuery.refresh(),
      catalogQuery.refresh(),
      securityQuery.refresh(),
      isLedger() ? ledger.query.refresh() : detail.refresh(),
    ]);
  };

  /** Uses project totals even when filters hide all loaded rows. */
  const hasLedgerRows = () => ledgerAllCount(ledger.counts()) > 0;
  const ledgerEmptyRead = () =>
    isLedger() && ledger.query.value() != null && !hasLedgerRows() && ledger.totalMatch() === 0;
  /** A background pass can invalidate the retained empty read before activation. */
  const ledgerHeld = () =>
    ledgerEmptyRead() && displayedPass() != null && !ledger.query.settledSinceActivation();
  const ledgerEmpty = () => ledgerEmptyRead() && !ledgerHeld();
  // The previous stage stays on screen until the activation read confirms or replaces the empty ledger.
  usePresentationParticipant("security-ledger", () => !ledgerHeld());
  const emptyReason = () =>
    emptyReasonFor({
      enabled: overview()?.enabled !== false,
      filtersActive: ledger.filtersActive(),
      running: displayedPass() != null,
      fullPassCompleted: Boolean(overview()?.last_full?.completed_at),
      changesScanned: (overview()?.scanners ?? []).some((scanner) => Boolean(scanner.last_completed_at)),
    });

  /** Counts and findings become available together. */
  const runSeverityCounts = (): Record<string, number> | undefined => {
    if (isLedger()) return undefined;
    const scan = selectedScan();
    if (scan?.status !== "complete" || !detail.results.value()) return undefined;
    return scan.findings_by_level;
  };

  const runMeta = () => {
    const scan = selectedScan();
    if (!scan) return null;
    if (runFiltersActive()) {
      return `${runFilteredFindings().length} of ${allFindings().length} findings`;
    }
    return scanFindingsCounts(scan);
  };

  return (
    <BrowseStagePanel
      back={props.back}
      appStore={props.appStore}
      requireClient={true}
      scrollHost="content"
      contentReady={() =>
        catalogQuery.ready() &&
        historyQuery.ready() &&
        securityQuery.ready() &&
        (isLedger() ? ledger.query.ready() : detail.summary.ready() && detail.results.ready())
      }
      testId="security-findings-pane"
      title={SECURITY_SCANNERS_SECTION_LABEL}
      error={error() ?? undefined}
      errorTestId="scans-error"
      primary={
        <div class="den-scans-primary" data-first-time-tip-anchor="project-security">
          {props.folderPicker}
          <Show
            when={!isLedger()}
            fallback={
              <Show when={!catalogOpen()}>
              <label class="den-scans-query">
                <span class="den-scans-query__glyph" aria-hidden="true">
                  ⌕
                </span>
                <input
                  type="search"
                  autocomplete="off"
                  spellcheck={false}
                  data-testid="ledger-filter-input"
                  aria-label="Filter findings"
                  placeholder="Filter findings — message, path, rule id, or advisory id"
                  value={ledger.text()}
                  onInput={(event) => ledger.setText(event.currentTarget.value)}
                />
              </label>
              </Show>
            }
          >
            <ScanRunPicker
              scans={history()}
              hasMore={Boolean(historyQuery.value()?.next_cursor)}
              loading={historyQuery.loading()}
              selectedId={selectedId()}
              selectedScan={selectedScan()}
              onSelect={selectScan}
              runSortKey={runSortKey()}
              runSortDir={runSortDir()}
              onRunSortKeyChange={(key) =>
                batch(() => {
                  setRunSortDir(nextRunSortDirection(runSortKey(), key, runSortDir()));
                  setRunSortKey(key);
                  resetHistory();
                })
              }
              onRunSortDirToggle={() =>
                batch(() => {
                  setRunSortDir((dir) => (dir === "asc" ? "desc" : "asc"));
                  resetHistory();
                })
              }
              historyPage={historyPage()}
              shownHistoryPage={shownHistoryPage()}
              onHistoryPageChange={(page) => {
                if (historyQuery.loading() || page < 0) return;
                if (page > historyPage()) {
                  const cursor = historyQuery.value()?.next_cursor;
                  if (!cursor) return;
                  setHistoryCursors((values) => [...values.slice(0, page), cursor]);
                }
                setHistoryPage(page);
              }}
              scannerLabels={scannerLabels()}
            />
          </Show>
        </div>
      }
      chips={
        <div class="den-scans-chrome-chips">
          <div class="den-scans-chrome-chips__filters">
            <Show
              when={isLedger()}
              fallback={
                <BrowseChip
                  label="Back to findings"
                  tone="action"
                  testId="scans-runs-back"
                  onClick={() =>
                    batch(() => {
                      setView("ledger");
                      setDrillId(null);
                    })
                  }
                />
              }
            >
              <Show
                when={!catalogOpen()}
                fallback={
                  <BrowseChip
                    label="Back to findings"
                    tone="action"
                    testId="ignore-catalog-back"
                    onClick={() => setCatalogOpen(false)}
                  />
                }
              >
              <BrowseSegmented
                options={[
                  {
                    id: "open",
                    label: `Open ${ledgerOpenCount(ledger.counts())}`,
                    testId: "ledger-tab-open",
                  },
                  {
                    id: "all",
                    label: `All ${ledgerAllCount(ledger.counts())}`,
                    testId: "ledger-tab-all",
                  },
                ]}
                value={ledger.tab()}
                onChange={(next) =>
                  batch(() => {
                    ledger.setTab(next as "open" | "all");
                    setLedgerKey(null);
                  })
                }
                ariaLabel="Findings"
                testId="ledger-tabs"
              />
              <FindingFilterChip
                ledger={ledger}
                scanners={overview()?.scanners ?? []}
                states={ledger.tab() === "open" ? OPEN_TAB_STATES : ALL_TAB_STATES}
                disabled={!hasLedgerRows() && !ledger.filtersActive()}
              />
              <Show when={ledger.filtersActive()}>
                <button
                  type="button"
                  class="den-scans-filter-clear"
                  data-testid="scans-filter-clear"
                  onClick={clearFilters}
                >
                  Clear
                </button>
              </Show>
              </Show>
            </Show>
            <Show when={runSeverityCounts()}>
              <div
                class="den-browse-filter-toggles den-scans-severity-filters"
                role="group"
                aria-label="Filter by severity"
                data-testid="scans-severity-chips"
              >
                <For each={kpiEntries(runSeverityCounts() ?? {})}>
                  {(row) => {
                    const on = () => severityFilter().has(row.level);
                    return (
                      <button
                        type="button"
                        class="den-browse-filter-toggle"
                        classList={{ "den-browse-filter-toggle--on": on() }}
                        data-testid={`scans-severity-chip-${row.level}`}
                        aria-pressed={on()}
                        aria-label={`${row.label} ${row.count}`}
                        onClick={() => toggleRunSeverity(row.level)}
                      >
                        {row.label} <span class="den-browse-filter-toggle__count">{row.count}</span>
                      </button>
                    );
                  }}
                </For>
              </div>
            </Show>
            <Show when={!isLedger() && runFiltersActive()}>
              <button
                type="button"
                class="den-scans-filter-clear"
                data-testid="scans-filter-clear-run"
                onClick={clearFilters}
              >
                Clear
              </button>
            </Show>
          </div>

          <div class="den-scans-chrome-meta">
            <Show
              when={!isLedger()}
              fallback={
                <Show when={hasLedgerRows() && !catalogOpen()}>
                  <span data-testid="ledger-counts">
                    {ledger.filtersActive() ? `${ledger.totalMatch()} shown · ` : ""}
                    {ledgerCountsSummary(ledger.counts())}
                  </span>
                </Show>
              }
            >
              <Show when={selectedScan()}>
                {(scan) => (
                  <>
                    <Show when={scan().status === "complete" && detail.results.value()}>
                      <span data-testid="scans-counts">{runMeta()}</span>
                    </Show>
                    <Show when={scan().scan_scope}>
                      <span data-testid="scans-scope"> · {scan().scan_scope}</span>
                    </Show>
                    <Show when={scansWantLiveRefresh(history(), props.liveScan, selectedScan())}>
                      <span role="status" data-testid="scans-live-refresh">
                        {" "}
                        · Live
                      </span>
                    </Show>
                  </>
                )}
              </Show>
            </Show>
          </div>

          <div class="den-scans-chrome-tools">
            <ShowLatest when={!isLedger() && selectedScan()} by={(scan) => scan.id}>
              {(scan) => (
                <ScanLimitations projectId={props.projectId} scan={scan()} repoRoot={props.repoRoot} client={client()} />
              )}
            </ShowLatest>
            <BrowseOverflowMenu testId="scans-overflow" variant="icon" items={overflowItems()} />
          </div>
        </div>
      }
      detailSurface={ledgerPaneSurface()}
      detail={(() => {
        if (isLedger()) {
          const entry = selectedLedgerEntry();
          return entry ? (
            <LedgerFindingDetail
              entry={entry}
              ledger={ledger}
              scannerLabels={scannerLabels()}
              repoRoot={props.repoRoot}
              onOpenRun={openRun}
              onFixWithAgent={() => void handOffToAgent([entry])}
              onAddToChat={() => void attachFindings([entry])}
              addingToChat={addingToChat()}
              onClose={() => setLedgerKey(null)}
            />
          ) : null;
        }
        const finding = selectedRunFinding();
        return finding ? (
          <RunFindingDetail
            finding={finding}
            onAddToChat={() => void attachFindings([{
              finding,
              last_scan_id: selectedScan()?.id,
            }])}
            addingToChat={addingToChat()}
            scan={selectedScan()}
            scannerLabels={scannerLabels()}
            repoRoot={props.repoRoot}
            onClose={() => setDrillId(null)}
          />
        ) : null;
      })()}
    >
      <Show when={loadError()}>
        <DenButton variant="link" onClick={() => void retryLoading()}>
          Retry loading
        </DenButton>
      </Show>

      <Show
        when={!(isLedger() && catalogOpen())}
        fallback={
          <IgnoreCatalogPanel
            catalog={ledger.ignores.value() ?? null}
            repoRoot={props.repoRoot}
            pending={ledger.ignorePending()}
            onWithdraw={(entryId) => void ledger.removeIgnore(entryId)}
          />
        }
      >
      <Show when={!ledgerHeld()}>
      <Show when={!ledgerEmpty()}>
      <SecurityCoverage
        projectId={props.projectId}
        overview={overview()}
        displayed={displayedPass()}
        newSince={isLedger() ? ledger.introducedSince() !== undefined : newSince()}
        passSuperseded={anyPassSuperseded(overview())}
        onToggleNewSince={() => {
          if (isLedger()) {
            ledger.setIntroducedSince(
              ledger.introducedSince() === undefined ? baselineTime() : undefined,
            );
            return;
          }
          setNewSince(!newSince());
          restartRunView();
        }}
        onStart={startFullScan}
        starting={startingFullScan()}
      />
      </Show>

      <Show when={ledgerEmpty()}>
        <SecurityEmptyState
          reason={emptyReason()}
          overview={overview()}
          displayed={displayedPass()}
          starting={startingFullScan()}
          onStartFullScan={() => void startFullScan([...defaultFullScanSelection(overview())])}
          onClearFilters={clearFilters}
        />
      </Show>

      <Show when={isLedger() && ledger.selected().size > 0}>
        <div class="den-ledger-selection" data-testid="ledger-selection">
          <span class="den-ledger-selection__count">
            {ledger.selected().size} selected
          </span>
          <div class="den-ledger-selection__actions">
            <DenButton
              variant="secondary"
              compact
              data-testid="ledger-selection-add-to-chat"
              disabled={addingToChat()}
              onClick={() => void attachFindings(ledger.selectedEntries())}
            >
              Add to chat
            </DenButton>
            <DenButton
              variant="primary"
              compact
              data-testid="ledger-selection-fix"
              onClick={() => void handOffToAgent(ledger.selectedEntries())}
            >
              Fix with agent
            </DenButton>
            <DenButton
              variant="secondary"
              compact
              data-testid="ledger-selection-ignore"
              ref={(element: HTMLButtonElement) => {
                ignoreAnchorEl = element;
              }}
              onClick={() => setIgnoreOpen((open) => !open)}
            >
              Ignore…
            </DenButton>
            <DenButton
              variant="ghost"
              compact
              data-testid="ledger-selection-clear"
              onClick={() => ledger.clearSelection()}
            >
              Clear
            </DenButton>
          </div>
          <Show when={ignoreOpen()}>
            <IgnoreFindingPanel
              entries={ledger.selectedEntries()}
              path={ledger.ignores.value()?.path}
              anchor={() => ignoreAnchorEl}
              pending={ledger.ignorePending()}
              onCommit={(entries) => {
                void ledger.addIgnores(entries).then((ok) => {
                  if (ok) setIgnoreOpen(false);
                });
              }}
              onDismiss={() => setIgnoreOpen(false)}
            />
          </Show>
        </div>
      </Show>

      <Show
        when={isLedger()}
        fallback={
          <RunFindingsList
            scan={selectedScan()}
            findings={runFilteredFindings()}
            loadedCount={allFindings().length}
            fixedFindings={detail.fixedFindings()}
            newSince={newSince()}
            findingsWithoutHistory={findingsWithoutHistory()}
            loading={detail.summary.coldPending() || detail.results.coldPending()}
            showLoading={detail.summary.showLoading() || detail.results.showLoading()}
            filtersActive={runFiltersActive()}
            showEmpty={!historyQuery.loading() && !detail.summary.error() && !detail.results.error()}
            onClearFilters={clearFilters}
            page={findingsPage()}
            onPageChange={setFindingsPage}
            sort={runSort()}
            onSort={(state) => {
              void commitListSort(SECURITY_LIST_SURFACE, state);
              restartRunView();
            }}
            selectedFingerprint={drillId()}
            onSelect={(fingerprint) =>
              setDrillId((current) => (current === fingerprint ? null : fingerprint))
            }
            repoRoot={props.repoRoot}
            listRef={(element) => {
              listScrollEl = element;
            }}
          />
        }
      >
        <LedgerFindingsList
          ledger={ledger}
          overview={overview()}
          repoRoot={props.repoRoot}
          selectedKey={ledgerKey()}
          onSelect={(entry) =>
            setLedgerKey((current) => {
              const key = ledgerEntryKey(entry);
              return current === key ? null : key;
            })
          }
          listRef={(element) => {
            listScrollEl = element;
          }}
        />
      </Show>
      </Show>
      </Show>
    </BrowseStagePanel>
  );
}
