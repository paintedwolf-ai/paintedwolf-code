import { For, Show } from "solid-js";
import type { FindingLedgerEntry, SecurityOverview } from "../../api/types.ts";
import { resolveColumnWidths } from "../../list/list-columns.ts";
import { createFittedColumns } from "../../list/create-fitted-columns.ts";
import type { SortState } from "../../list/list-sort.ts";
import { LEDGER_COLUMNS, LEDGER_LIST_SURFACE } from "../../lib/scan-columns.ts";
import {
  formatLocation,
  levelChipClass,
  levelLabel,
  primaryLocation,
} from "../../lib/scan-display.ts";
import {
  ledgerEmptyLine,
  ledgerEntryKey,
  ledgerStateChipClass,
  ledgerStateIsAbsent,
  ledgerStateLabel,
} from "../../lib/ledger-display.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { listPanePrefs } from "../../shell/layout-store.ts";
import { ListColumnHeader } from "../list/ListColumnHeader.tsx";
import { TablePager } from "../list/TablePager.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import type { FindingLedgerModel } from "./create-finding-ledger.ts";

type Props = {
  ledger: FindingLedgerModel;
  overview: SecurityOverview | null;
  repoRoot?: string;
  selectedKey: string | null;
  onSelect: (entry: FindingLedgerEntry) => void;
  /** Reset scroll offset when results change. */
  listRef: (element: HTMLDivElement) => void;
};

export function LedgerFindingsList(props: Props) {
  const widths = () =>
    resolveColumnWidths(LEDGER_COLUMNS, listPanePrefs(LEDGER_LIST_SURFACE).columnWidths);
  const fitted = createFittedColumns({ columns: () => LEDGER_COLUMNS, widths });
  const ledger = () => props.ledger;
  const loading = () => ledger().query.showLoading();
  const entries = () => ledger().entries();

  const applySort = (state: SortState) => ledger().setSort(state);

  return (
    <section
      class="den-scans-list-pane"
      aria-busy={ledger().query.loading()}
      data-testid="ledger-pane"
    >
      <Scrollport
        class="den-scans-list-scroll"
        axis="both"
        data-testid="ledger-scroll"
        viewportRef={props.listRef}
      >
        <Show when={loading()}>
          <p class="den-scans-list-empty" role="status">
            Loading findings…
          </p>
        </Show>
        <Show when={!loading() && ledger().query.value() && entries().length === 0}>
          <p class="den-scans-list-empty" role="status" data-testid="ledger-list-empty">
            {ledger().filtersActive()
              ? "No findings match these filters."
              : ledgerEmptyLine(ledger().counts(), props.overview)}
          </p>
        </Show>
        <Show when={!loading() && entries().length > 0}>
          <ListColumnHeader
            surface={LEDGER_LIST_SURFACE}
            columns={fitted.columns()}
            widths={widths()}
            measureRef={fitted.measure}
            sort={ledger().sort()}
            onSort={applySort}
            ariaLabel="Finding columns"
            testId="ledger-header"
            renderCell={(column) =>
              column.key === "select" ? (
                <label class="den-ledger-check" data-testid="ledger-select-all-label">
                  <DenCheckboxControl
                    checked={ledger().allOnPageSelected()}
                    aria-label="Select every finding on this page"
                    data-testid="ledger-select-all"
                    onChange={() => ledger().toggleSelectAllOnPage()}
                  />
                </label>
              ) : undefined
            }
          />
          <ul class="den-scans-list" data-testid="ledger-rows">
            <For each={entries()}>
              {(entry) => {
                const key = ledgerEntryKey(entry);
                const location = () =>
                  formatLocation(primaryLocation(entry.finding), props.repoRoot);
                const checked = () => ledger().selected().has(key);
                return (
                  <li
                    class="den-ledger-row"
                    classList={{ "den-ledger-row--absent": ledgerStateIsAbsent(entry.state) }}
                  >
                    {/* Keep checkbox click target separate from row selection. */}
                    <label class="den-ledger-check" data-testid="ledger-row-check-label">
                      <DenCheckboxControl
                        checked={checked()}
                        aria-label={`Select ${entry.finding.message}`}
                        data-testid="ledger-row-check"
                        onChange={() => ledger().toggleSelected(entry)}
                      />
                    </label>
                    <button
                      type="button"
                      class="den-scans-row den-list-row"
                      classList={{ "den-scans-row--active": props.selectedKey === key }}
                      style={{ "--den-list-cols": fitted.template() }}
                      data-testid="ledger-row"
                      data-state={entry.state}
                      onClick={() => props.onSelect(entry)}
                    >
                      <span class="den-list-cell" data-column="select" aria-hidden="true" />
                      <span class="den-list-cell" data-column="severity">
                        <span class={`den-scans-level ${levelChipClass(entry.finding.level)}`}>
                          {levelLabel(entry.finding.level)}
                        </span>
                      </span>
                      <span class="den-list-cell den-scans-row__msg" data-column="finding">
                        {entry.finding.message}
                      </span>
                      <span class="den-list-cell den-scans-row__meta" data-column="location">
                        {location()}
                      </span>
                      <span class="den-list-cell" data-column="state">
                        <span class={ledgerStateChipClass(entry.state)}>
                          <span class="den-ledger-state__dot" aria-hidden="true" />
                          {ledgerStateLabel(entry.state)}
                        </span>
                        <Show when={entry.ignore?.expired}>
                          <span class="den-ledger-ignore-mark" data-testid="ledger-row-ignore-lapsed">
                            {" · ignore lapsed"}
                          </span>
                        </Show>
                      </span>
                      <Show when={fitted.shows("last_seen")}>
                        <span class="den-list-cell den-scans-row__meta" data-column="last_seen">
                          {relativeTimeLabel(entry.last_seen_at)}
                        </span>
                      </Show>
                    </button>
                  </li>
                );
              }}
            </For>
          </ul>
        </Show>
      </Scrollport>
      <Show when={!loading() && ledger().totalMatch() > 0}>
        <TablePager
          testId="ledger-pager"
          page={ledger().page()}
          shownPage={ledger().shownPage()}
          pageSize={ledger().pageSize}
          total={ledger().totalMatch()}
          canNext={ledger().canNext()}
          loading={ledger().query.loading()}
          onPageChange={(next) => ledger().setPage(next)}
          ariaLabel="Finding pagination"
        />
      </Show>
    </section>
  );
}
