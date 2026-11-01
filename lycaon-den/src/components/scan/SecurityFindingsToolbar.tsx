import { For, Show, batch, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  CodeScan,
  CodeScanEvent,
  FindingLedgerState,
  FindingLevel,
  SecurityOverview,
} from "../../api/types.ts";
import type { ContextMenuItem } from "../ContextMenu.tsx";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";
import { BrowseChip } from "../browse/BrowseChip.tsx";
import { BrowseSegmented } from "../browse/BrowseSegmented.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import {
  kpiEntries,
  scanFindingsCounts,
  scansWantLiveRefresh,
} from "../../lib/scan-display.ts";
import {
  ledgerAllCount,
  ledgerCountsSummary,
  ledgerOpenCount,
} from "../../lib/ledger-display.ts";
import { type RunSortKey } from "../../lib/scan-findings-table.ts";
import type { SortDirection } from "../../list/list-sort.ts";
import { FindingFilterChip } from "./FindingFilterChip.tsx";
import { ScanLimitations } from "./ScanLimitations.tsx";
import { ScanRunPicker } from "./ScanRunPicker.tsx";
import type { FindingLedgerModel } from "./create-finding-ledger.ts";

const OPEN_TAB_STATES: FindingLedgerState[] = ["open", "reopened"];
const ALL_TAB_STATES: FindingLedgerState[] = [
  "open",
  "reopened",
  "ignored",
  "fixed",
  "unverified",
  "not_observed",
];

export type SecurityFindingsPrimaryBarProps = {
  folderPicker?: JSX.Element;
  isLedger: boolean;
  catalogOpen: boolean;
  ledger: FindingLedgerModel;
  scans: CodeScan[];
  hasMore: boolean;
  loading: boolean;
  selectedId: string | null;
  selectedScan: CodeScan | null;
  onSelectScan: (scanId: string) => void;
  runSortKey: RunSortKey;
  runSortDir: SortDirection;
  onRunSortKeyChange: (key: RunSortKey) => void;
  onRunSortDirToggle: () => void;
  historyPage: number;
  shownHistoryPage: number;
  onHistoryPageChange: (page: number) => void;
  scannerLabels: Record<string, string>;
};

export function SecurityFindingsPrimaryBar(props: SecurityFindingsPrimaryBarProps) {
  return (
    <div class="den-scans-primary" data-first-time-tip-anchor="project-security">
      {props.folderPicker}
      <Show
        when={!props.isLedger}
        fallback={
          <Show when={!props.catalogOpen}>
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
                value={props.ledger.text()}
                onInput={(event) => props.ledger.setText(event.currentTarget.value)}
              />
            </label>
          </Show>
        }
      >
        <ScanRunPicker
          scans={props.scans}
          hasMore={props.hasMore}
          loading={props.loading}
          selectedId={props.selectedId}
          selectedScan={props.selectedScan}
          onSelect={props.onSelectScan}
          runSortKey={props.runSortKey}
          runSortDir={props.runSortDir}
          onRunSortKeyChange={props.onRunSortKeyChange}
          onRunSortDirToggle={props.onRunSortDirToggle}
          historyPage={props.historyPage}
          shownHistoryPage={props.shownHistoryPage}
          onHistoryPageChange={props.onHistoryPageChange}
          scannerLabels={props.scannerLabels}
        />
      </Show>
    </div>
  );
}

export type SecurityFindingsChipsBarProps = {
  projectId: string | undefined;
  repoRoot: string | undefined;
  client: LycaonClient | undefined;
  isLedger: boolean;
  catalogOpen: boolean;
  ledger: FindingLedgerModel;
  overview: SecurityOverview | null;
  hasLedgerRows: boolean;
  selectedScan: CodeScan | null;
  scanResultsReady: boolean;
  runSeverityCounts: Record<string, number> | undefined;
  severityFilter: ReadonlySet<FindingLevel>;
  runFiltersActive: boolean;
  runFilteredCount: number;
  allFindingsCount: number;
  history: CodeScan[];
  liveScan: CodeScanEvent | undefined;
  overflowItems: ContextMenuItem[];
  onBackFromRuns: () => void;
  onCloseCatalog: () => void;
  onSelectLedgerTab: (next: "open" | "all") => void;
  onToggleRunSeverity: (level: FindingLevel) => void;
  onClearFilters: () => void;
};

export function SecurityFindingsChipsBar(props: SecurityFindingsChipsBarProps) {
  const runMeta = () => {
    const scan = props.selectedScan;
    if (!scan) return null;
    if (props.runFiltersActive) {
      return `${props.runFilteredCount} of ${props.allFindingsCount} findings`;
    }
    return scanFindingsCounts(scan);
  };

  return (
    <div class="den-scans-chrome-chips">
      <div class="den-scans-chrome-chips__filters">
        <Show
          when={props.isLedger}
          fallback={
            <BrowseChip
              label="Back to findings"
              tone="action"
              testId="scans-runs-back"
              onClick={() => batch(props.onBackFromRuns)}
            />
          }
        >
          <Show
            when={!props.catalogOpen}
            fallback={
              <BrowseChip
                label="Back to findings"
                tone="action"
                testId="ignore-catalog-back"
                onClick={props.onCloseCatalog}
              />
            }
          >
            <BrowseSegmented
              options={[
                {
                  id: "open",
                  label: `Open ${ledgerOpenCount(props.ledger.counts())}`,
                  testId: "ledger-tab-open",
                },
                {
                  id: "all",
                  label: `All ${ledgerAllCount(props.ledger.counts())}`,
                  testId: "ledger-tab-all",
                },
              ]}
              value={props.ledger.tab()}
              onChange={(next) =>
                batch(() => props.onSelectLedgerTab(next as "open" | "all"))
              }
              ariaLabel="Findings"
              testId="ledger-tabs"
            />
            <FindingFilterChip
              ledger={props.ledger}
              scanners={props.overview?.scanners ?? []}
              states={props.ledger.tab() === "open" ? OPEN_TAB_STATES : ALL_TAB_STATES}
              disabled={!props.hasLedgerRows && !props.ledger.filtersActive()}
            />
            <Show when={props.ledger.filtersActive()}>
              <button
                type="button"
                class="den-scans-filter-clear"
                data-testid="scans-filter-clear"
                onClick={props.onClearFilters}
              >
                Clear
              </button>
            </Show>
          </Show>
        </Show>
        <Show when={props.runSeverityCounts}>
          <div
            class="den-browse-filter-toggles den-scans-severity-filters"
            role="group"
            aria-label="Filter by severity"
            data-testid="scans-severity-chips"
          >
            <For each={kpiEntries(props.runSeverityCounts ?? {})}>
              {(row) => {
                const on = () => props.severityFilter.has(row.level);
                return (
                  <button
                    type="button"
                    class="den-browse-filter-toggle"
                    classList={{ "den-browse-filter-toggle--on": on() }}
                    data-testid={`scans-severity-chip-${row.level}`}
                    aria-pressed={on()}
                    aria-label={`${row.label} ${row.count}`}
                    onClick={() => props.onToggleRunSeverity(row.level)}
                  >
                    {row.label}{" "}
                    <span class="den-browse-filter-toggle__count">{row.count}</span>
                  </button>
                );
              }}
            </For>
          </div>
        </Show>
        <Show when={!props.isLedger && props.runFiltersActive}>
          <button
            type="button"
            class="den-scans-filter-clear"
            data-testid="scans-filter-clear-run"
            onClick={props.onClearFilters}
          >
            Clear
          </button>
        </Show>
      </div>

      <div class="den-scans-chrome-meta">
        <Show
          when={!props.isLedger}
          fallback={
            <Show when={props.hasLedgerRows && !props.catalogOpen}>
              <span data-testid="ledger-counts">
                {props.ledger.filtersActive()
                  ? `${props.ledger.totalMatch()} shown · `
                  : ""}
                {ledgerCountsSummary(props.ledger.counts())}
              </span>
            </Show>
          }
        >
          <Show when={props.selectedScan}>
            {(scan) => (
              <>
                <Show when={scan().status === "complete" && props.scanResultsReady}>
                  <span data-testid="scans-counts">{runMeta()}</span>
                </Show>
                <Show when={scan().scan_scope}>
                  <span data-testid="scans-scope"> · {scan().scan_scope}</span>
                </Show>
                <Show
                  when={scansWantLiveRefresh(
                    props.history,
                    props.liveScan,
                    props.selectedScan,
                  )}
                >
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
        <ShowLatest
          when={!props.isLedger && props.selectedScan}
          by={(scan) => scan.id}
        >
          {(scan) => (
            <ScanLimitations
              projectId={props.projectId}
              scan={scan()}
              repoRoot={props.repoRoot}
              client={props.client}
            />
          )}
        </ShowLatest>
        <BrowseOverflowMenu
          testId="scans-overflow"
          variant="icon"
          items={props.overflowItems}
        />
      </div>
    </div>
  );
}
