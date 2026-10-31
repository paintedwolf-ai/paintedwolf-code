import {
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
  FindingLevel,
  ProjectRoot,
  SecurityFinding,
  SecurityOverview,
} from "../../api/types.ts";
import { createScanDetail } from "./create-scan-detail.ts";
import { createFindingLedger } from "./create-finding-ledger.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import {
  FINDINGS_PAGE_SIZE,
  HISTORY_PAGE_SIZE,
  type RunSortKey,
  filterFindingsByLevels,
  nextRunSortDirection,
} from "../../lib/scan-findings-table.ts";
import type { SortDirection } from "../../list/list-sort.ts";
import {
  pickDefaultScanId,
  SCANS_LIVE_REFRESH_MS,
  SCANS_LIVE_REFRESH_MAX_MS,
  scansWantLiveRefresh,
} from "../../lib/scan-display.ts";
import {
  ledgerAllCount,
  ledgerEntryKey,
} from "../../lib/ledger-display.ts";
import { createLiveScanListGate } from "../../lib/scan-live-refresh.ts";
import {
  filterFindingsNewSince,
  securityBaselineTime,
} from "../../lib/scan-coverage.ts";
import {
  onFindingFocusRequest,
  takeFindingFocusRequest,
} from "../../platform/navigation/open-finding.ts";
import { useSettingsBackend } from "../../settings/settings-backend.ts";
import { SECURITY_SCANNERS_SECTION_LABEL } from "../../settings/settings-nav-model.ts";
import { scannerJobLabel } from "../../settings/extensions/scanners-catalog-model.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { emptyReasonFor } from "./SecurityEmptyState.tsx";
import { createDisplayedFullPass } from "./displayed-full-pass.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { createAdaptivePoller } from "../../store/adaptive-poller.ts";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { selectedSecurityFolder, selectSecurityFolder } from "../../platform/files/security-folder-selection.ts";
import { rootScanClient } from "./root-scan-client.ts";
import { createProjectSecurityQuery, securityOverviewRunning } from "./project-security-query.ts";
import { useSecurityFindingsActions } from "./useSecurityFindingsActions.ts";
import {
  SecurityFindingsChipsBar,
  SecurityFindingsPrimaryBar,
} from "./SecurityFindingsToolbar.tsx";
import {
  SecurityFindingsRunsStage,
  renderSecurityFindingsDetailPane,
  securityDetailSurface,
} from "./SecurityFindingsRunsStage.tsx";

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

export function SecurityFindingsPane(props: Props) {
  const root = createMemo(
    () =>
      props.roots?.find((entry) => entry.id === selectedSecurityFolder(props.projectId)) ??
      props.roots?.find((entry) => entry.is_primary) ??
      props.roots?.[0],
  );
  const identity = () => JSON.stringify([props.projectId, root()?.id]);
  return (
    <Show when={identity()} keyed>
      {(_identity) => (
        <FolderSecurityFindingsPane
          {...props}
          rootId={root()?.id}
          repoRoot={root()?.path ?? props.repoRoot}
          folderPicker={
            <Show when={(props.roots?.length ?? 0) > 1}>
              <DenSelect
                aria-label="Security folder"
                value={root()?.id ?? ""}
                options={(props.roots ?? []).map((entry) => ({
                  value: entry.id,
                  label: entry.label,
                }))}
                onValueChange={(id) => {
                  if (props.projectId) selectSecurityFolder(props.projectId, id);
                }}
              />
            </Show>
          }
        />
      )}
    </Show>
  );
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
        ? {
            client: c,
            projectId,
            sort,
            order,
            cursor,
            key: JSON.stringify([projectId, sort, order, cursor]),
          }
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

  const { addingToChat, attachFindings, handOffToAgent, overflowItems } =
    useSecurityFindingsActions({
      client,
      projectId: () => props.projectId,
      rootId: () => props.rootId,
      repoRoot: () => props.repoRoot,
      roots: () => props.roots,
      isLedger,
      ledger,
      selectedScan,
      scanResultsValue: () => detail.results.value(),
      scanResultsLoading: () => detail.results.loading(),
      allFindings,
      runFilteredFindings,
      runFiltersActive,
      setError,
      onOpenCatalog: () => {
        setCatalogOpen(true);
        setLedgerKey(null);
        ledger.clearSelection();
      },
      onOpenRuns: () => {
        setView("runs");
        setLedgerKey(null);
      },
      onBackToLedger: () => {
        setView("ledger");
        setDrillId(null);
      },
    });

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
      changesScanned: (overview()?.scanners ?? []).some((scanner) =>
        Boolean(scanner.last_completed_at),
      ),
    });

  /** Counts and findings become available together. */
  const runSeverityCounts = (): Record<string, number> | undefined => {
    if (isLedger()) return undefined;
    const scan = selectedScan();
    if (scan?.status !== "complete" || !detail.results.value()) return undefined;
    return scan.findings_by_level;
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
        <SecurityFindingsPrimaryBar
          folderPicker={props.folderPicker}
          isLedger={isLedger()}
          catalogOpen={catalogOpen()}
          ledger={ledger}
          scans={history()}
          hasMore={Boolean(historyQuery.value()?.next_cursor)}
          loading={historyQuery.loading()}
          selectedId={selectedId()}
          selectedScan={selectedScan()}
          onSelectScan={selectScan}
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
      }
      chips={
        <SecurityFindingsChipsBar
          projectId={props.projectId}
          repoRoot={props.repoRoot}
          client={client()}
          isLedger={isLedger()}
          catalogOpen={catalogOpen()}
          ledger={ledger}
          overview={overview()}
          hasLedgerRows={hasLedgerRows()}
          selectedScan={selectedScan()}
          scanResultsReady={Boolean(detail.results.value())}
          runSeverityCounts={runSeverityCounts()}
          severityFilter={severityFilter()}
          runFiltersActive={runFiltersActive()}
          runFilteredCount={runFilteredFindings().length}
          allFindingsCount={allFindings().length}
          history={history()}
          liveScan={props.liveScan}
          overflowItems={overflowItems()}
          onBackFromRuns={() => {
            setView("ledger");
            setDrillId(null);
          }}
          onCloseCatalog={() => setCatalogOpen(false)}
          onSelectLedgerTab={(next) => {
            ledger.setTab(next);
            setLedgerKey(null);
          }}
          onToggleRunSeverity={toggleRunSeverity}
          onClearFilters={clearFilters}
        />
      }
      detailSurface={securityDetailSurface(isLedger())}
      detail={renderSecurityFindingsDetailPane({
        isLedger: isLedger(),
        selectedLedgerEntry: selectedLedgerEntry(),
        selectedRunFinding: selectedRunFinding(),
        selectedScan: selectedScan(),
        ledger,
        scannerLabels: scannerLabels(),
        repoRoot: props.repoRoot,
        addingToChat: addingToChat(),
        onOpenRun: openRun,
        onHandOffToAgent: (entries) => void handOffToAgent(entries),
        onAttachFindings: (entries) => void attachFindings(entries),
        onCloseLedgerDetail: () => setLedgerKey(null),
        onCloseRunDetail: () => setDrillId(null),
      })}
    >
      <SecurityFindingsRunsStage
        projectId={props.projectId}
        repoRoot={props.repoRoot}
        isLedger={isLedger()}
        catalogOpen={catalogOpen()}
        ledgerHeld={ledgerHeld()}
        ledgerEmpty={ledgerEmpty()}
        emptyReason={emptyReason()}
        loadError={loadError()}
        onRetryLoading={() => void retryLoading()}
        ledger={ledger}
        overview={overview()}
        displayedPass={displayedPass()}
        newSince={newSince()}
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
        startingFullScan={startingFullScan()}
        onStartFullScan={startFullScan}
        addingToChat={addingToChat()}
        onAttachFindings={(entries) => void attachFindings(entries)}
        onHandOffToAgent={(entries) => void handOffToAgent(entries)}
        ignoreOpen={ignoreOpen()}
        setIgnoreOpen={setIgnoreOpen}
        selectedScan={selectedScan()}
        runFilteredFindings={runFilteredFindings()}
        allFindingsCount={allFindings().length}
        fixedFindings={detail.fixedFindings()}
        findingsWithoutHistory={findingsWithoutHistory()}
        runLoading={detail.summary.coldPending() || detail.results.coldPending()}
        runShowLoading={detail.summary.showLoading() || detail.results.showLoading()}
        runFiltersActive={runFiltersActive()}
        runShowEmpty={
          !historyQuery.loading() && !detail.summary.error() && !detail.results.error()
        }
        onClearFilters={clearFilters}
        findingsPage={findingsPage()}
        onPageChange={setFindingsPage}
        onRestartRunView={restartRunView}
        drillId={drillId()}
        onSelectRunFinding={(fingerprint) =>
          setDrillId((current) => (current === fingerprint ? null : fingerprint))
        }
        ledgerKey={ledgerKey()}
        onSelectLedgerKey={setLedgerKey}
        onListRef={(element) => {
          listScrollEl = element;
        }}
      />
    </BrowseStagePanel>
  );
}
