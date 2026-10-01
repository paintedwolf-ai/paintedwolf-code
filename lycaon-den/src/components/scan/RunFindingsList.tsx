import { For, Show, createMemo } from "solid-js";
import type { CodeScan, SecurityFinding } from "../../api/types.ts";
import { resolveColumnWidths, sortRows } from "../../list/list-columns.ts";
import { createFittedColumns } from "../../list/create-fitted-columns.ts";
import type { SortState } from "../../list/list-sort.ts";
import { paginateSlice } from "../../list/pagination.ts";
import {
  SECURITY_COLUMNS,
  SECURITY_LIST_SURFACE,
  defaultSecurityOrder,
} from "../../lib/scan-columns.ts";
import { FINDINGS_PAGE_SIZE } from "../../lib/scan-findings-table.ts";
import {
  formatLocation,
  levelChipClass,
  levelLabel,
  primaryLocation,
  scanEmptyState,
} from "../../lib/scan-display.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { listPanePrefs } from "../../shell/layout-store.ts";
import { ListColumnHeader } from "../list/ListColumnHeader.tsx";
import { TablePager } from "../list/TablePager.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

type Props = {
  scan: CodeScan | null;
  /** Findings the run reported, already filtered by the stage's chips. */
  findings: SecurityFinding[];
  loadedCount: number;
  fixedFindings: SecurityFinding[];
  newSince: boolean;
  findingsWithoutHistory: number;
  loading: boolean;
  showLoading: boolean;
  filtersActive: boolean;
  /** Suppresses empty state while findings or history are unresolved. */
  showEmpty: boolean;
  onClearFilters: () => void;
  page: number;
  onPageChange: (page: number) => void;
  sort: SortState;
  onSort: (state: SortState) => void;
  selectedFingerprint: string | null;
  onSelect: (fingerprint: string) => void;
  repoRoot?: string;
  listRef: (element: HTMLDivElement) => void;
};

export function RunFindingsList(props: Props) {
  const widths = () =>
    resolveColumnWidths(SECURITY_COLUMNS, listPanePrefs(SECURITY_LIST_SURFACE).columnWidths);
  // Align track and row cell column layout.
  const fitted = createFittedColumns({ columns: () => SECURITY_COLUMNS, widths });

  // Severity order until a column sort is active.
  const ordered = createMemo(() => {
    const sort = props.sort;
    return sort
      ? sortRows(props.findings, SECURITY_COLUMNS, sort)
      : defaultSecurityOrder(props.findings);
  });
  const pageRows = createMemo(() => paginateSlice(ordered(), props.page, FINDINGS_PAGE_SIZE).slice);

  return (
    <section
      class="den-scans-list-pane"
      aria-busy={props.loading}
      data-testid="run-findings-pane"
    >
      <Scrollport
        class="den-scans-list-scroll"
        axis="both"
        data-testid="scans-findings-scroll"
        viewportRef={props.listRef}
      >
        <Show when={props.scan?.detail_pruned_at && props.loadedCount > 0}><p class="den-scans-list-empty" role="status">Scan details were pruned. Displayed findings are cached; run a new scan to inspect current findings.</p></Show>
        <Show when={props.newSince && props.findingsWithoutHistory > 0}>
          <p
            class="den-scans-list-empty"
            role="status"
            data-testid="scans-history-unavailable"
          >
            Introduction time is unavailable for {props.findingsWithoutHistory}{" "}
            {props.findingsWithoutHistory === 1 ? "finding" : "findings"}. Clear New since to
            include them.
          </p>
        </Show>
        <Show when={props.showLoading}>
          <p class="den-scans-list-empty" role="status">
            Loading findings…
          </p>
        </Show>
        <Show
          when={!props.loading && props.loadedCount > 0}
          fallback={
            <Show when={!props.loading && props.showEmpty}>
              <div class="den-scans-list-empty" role="status" data-testid="scans-empty">
                <p class="den-scans-empty-title">{scanEmptyState(props.scan).title}</p>
                <Show when={scanEmptyState(props.scan).description}>
                  {(description) => (
                    <p class="den-scans-empty-description">{description()}</p>
                  )}
                </Show>
              </div>
            </Show>
          }
        >
          <Show
            when={props.findings.length > 0}
            fallback={
              <p class="den-scans-list-empty" role="status" data-testid="scans-filter-empty">
                {props.newSince
                  ? "No findings with a recorded introduction since the baseline."
                  : "No findings match severity filter."}{" "}
                <button
                  type="button"
                  class="den-scans-filter-clear"
                  onClick={() => props.onClearFilters()}
                >
                  Clear filter
                </button>
              </p>
            }
          >
            <ListColumnHeader
              surface={SECURITY_LIST_SURFACE}
              columns={fitted.columns()}
              widths={widths()}
              measureRef={fitted.measure}
              sort={props.sort}
              onSort={props.onSort}
              ariaLabel="Findings columns"
              testId="scans-findings-header"
            />
            <ul class="den-scans-list" data-testid="scans-findings-list">
              <For each={pageRows()}>
                {(finding) => {
                  const location = () =>
                    formatLocation(primaryLocation(finding), props.repoRoot);
                  const introduced = () => relativeTimeLabel(finding.history?.introduced_at);
                  return (
                    <li>
                      <button
                        type="button"
                        class="den-scans-row den-list-row"
                        classList={{
                          "den-scans-row--active":
                            props.selectedFingerprint === finding.fingerprints.primary,
                        }}
                        style={{ "--den-list-cols": fitted.template() }}
                        data-testid="scans-finding-row"
                        onClick={() => props.onSelect(finding.fingerprints.primary)}
                      >
                        <span class="den-list-cell" data-column="severity">
                          <span class={`den-scans-level ${levelChipClass(finding.level)}`}>
                            {levelLabel(finding.level)}
                          </span>
                        </span>
                        <span class="den-list-cell den-scans-row__msg" data-column="message">
                          {finding.message}
                        </span>
                        <span class="den-list-cell den-scans-row__meta" data-column="location">
                          {location()}
                          <Show when={introduced()}>
                            {(ago) => (
                              <span data-testid="scans-finding-introduced">
                                {" "}
                                · introduced {ago()}
                              </span>
                            )}
                          </Show>
                        </span>
                        <Show when={fitted.shows("hint")}>
                          <span class="den-list-cell den-scans-row__meta" data-column="hint">
                            {finding.properties?.lycaon?.hint_code ?? ""}
                          </span>
                        </Show>
                      </button>
                    </li>
                  );
                }}
              </For>
            </ul>
          </Show>
        </Show>
        <Show when={props.newSince && props.fixedFindings.length > 0}>
          <section class="den-scans-fixed" data-testid="scans-fixed">
            <p class="den-scans-fixed__title">Fixed since baseline</p>
            <ul
              class="m-0 flex list-none flex-col gap-1 p-0"
              data-testid="scans-fixed-list"
            >
              <For each={props.fixedFindings}>
                {(finding) => (
                  <li class="den-scans-fixed__row" data-testid="scans-fixed-row">
                    <span>{finding.message}</span>
                    <span>{formatLocation(primaryLocation(finding), props.repoRoot)}</span>
                  </li>
                )}
              </For>
            </ul>
          </section>
        </Show>
      </Scrollport>
      <Show when={!props.loading && props.findings.length > 0}>
        <TablePager
          testId="scans-findings-pager"
          page={props.page}
          pageSize={FINDINGS_PAGE_SIZE}
          total={props.findings.length}
          onPageChange={props.onPageChange}
          ariaLabel="Findings pagination"
        />
      </Show>
    </section>
  );
}
