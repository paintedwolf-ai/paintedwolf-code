import {
  For,
  Show,
  createSignal,
  createEffect,
  createUniqueId,
} from "solid-js";
import type { CodeScan } from "../../api/types.ts";
import {
  type RunSortKey,
} from "../../lib/scan-findings-table.ts";
import type { SortDirection } from "../../list/list-sort.ts";
import {
  formatScanEngine,
  formatScanStatus,
  formatScanTimestamp,
  scanFindingsCounts,
} from "../../lib/scan-display.ts";
import { DenMenuTrigger } from "../primitives/DenMenuTrigger.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { TablePager } from "../list/TablePager.tsx";

type ScanRunPickerProps = {
  scans: CodeScan[];
  selectedId: string | null;
  selectedScan: CodeScan | null;
  onSelect: (id: string) => void;
  runSortKey: RunSortKey;
  runSortDir: SortDirection;
  onRunSortKeyChange: (key: RunSortKey) => void;
  onRunSortDirToggle: () => void;
  historyPage: number;
  /** The history page whose runs are listed while `historyPage` loads. */
  shownHistoryPage?: number;
  hasMore?: boolean;
  loading?: boolean;
  onHistoryPageChange: (page: number) => void;
  scannerLabels: Readonly<Record<string, string>>;
};

export function ScanRunPicker(props: ScanRunPickerProps) {
  const id = createUniqueId();
  const listboxId = `scan-runs-${id}`;
  const [open, setOpen] = createSignal(false);
  const [activeRun, setActiveRun] = createSignal(0);
  let triggerEl: HTMLButtonElement | undefined;
  createEffect(() => {
    const count = props.scans.length;
    setActiveRun((held) => Math.min(held, Math.max(0, count - 1)));
  });

  const resetActiveRun = () => {
    const selected = props.scans.findIndex(
      (scan) => scan.id === props.selectedId,
    );
    setActiveRun(selected >= 0 ? selected : 0);
  };

  const focusRunList = () => {
    queueMicrotask(() => {
      const listbox = document.getElementById(listboxId);
      listbox?.focus();
      listbox
        ?.querySelector<HTMLElement>(
          `[data-run-index="${activeRun()}"]`,
        )
        ?.scrollIntoView({ block: "nearest" });
    });
  };

  const show = () => {
    resetActiveRun();
    setOpen(true);
  };

  const close = (refocus = false) => {
    setOpen(false);
    if (refocus) queueMicrotask(() => triggerEl?.focus());
  };

  const closedLabel = () => {
    const scan = props.selectedScan;
    if (!scan) {
      return props.scans.length === 0 ? "No scan runs" : "Select a run";
    }
    const engine = formatScanEngine(scan, props.scannerLabels);
    const time = formatScanTimestamp(scan.created_at);
    return `Run: ${engine} · ${formatScanStatus(scan.status, scan.long_running)} · ${time}`;
  };

  const pickRun = (id: string) => {
    props.onSelect(id);
    close(true);
  };

  const onRunListKeyDown = (event: KeyboardEvent) => {
    const last = props.scans.length - 1;
    if (last < 0) return;
    let next = activeRun();
    if (event.key === "ArrowDown") next = Math.min(last, next + 1);
    else if (event.key === "ArrowUp") next = Math.max(0, next - 1);
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = last;
    else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      const scan = props.scans[activeRun()];
      if (scan) pickRun(scan.id);
      return;
    } else return;
    event.preventDefault();
    setActiveRun(next);
    focusRunList();
  };

  const dirGlyph = () => (props.runSortDir === "asc" ? "↑" : "↓");

  return (
    <div class="relative min-w-0 flex-1">
      <DenMenuTrigger
        label={closedLabel()}
        open={open()}
        popup="listbox"
        controls={listboxId}
        fill
        testId="scans-run-picker"
        disabled={props.scans.length === 0 && !props.selectedScan}
        buttonRef={(el) => {
          triggerEl = el;
        }}
        onClick={() => (open() ? close() : show())}
        onKeyDown={(event) => {
          if (!open() && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
            event.preventDefault();
            show();
          }
        }}
      />
      <Show when={open()}>
        <AnchoredSurface
          class="den-menu-surface flex max-h-[min(420px,60vh)] flex-col gap-1.5 p-2"
          anchor={() => triggerEl}
          preferredSide="bottom"
          align="start"
          width="min-anchor"
          overflow="hidden"
          minWidth={280}
          dismissOnScroll
          onDismiss={() => close()}
          onEscape={() => close(true)}
          ref={() => focusRunList()}
        >
            <div class="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1.5 border-b border-[var(--den-line)] px-1 py-0.5 pb-1.5">
              <label class="inline-flex items-center gap-1.5 text-den-hint text-den-text-muted">
                <span class="text-den-hint font-medium text-den-text-muted">
                  Sort
                </span>
                <DenSelect
                  aria-label="Sort scan history"
                  data-testid="scans-history-sort"
                  value={props.runSortKey}
                  options={[
                    { value: "date", label: "Created" },
                    { value: "engine", label: "Engine ID" },
                    { value: "status", label: "Status" },
                  ]}
                  onValueChange={(value) =>
                    props.onRunSortKeyChange(value as RunSortKey)
                  }
                />
              </label>
              <button
                type="button"
                class="rounded-den-sm border-0 bg-transparent px-1.5 py-0.5 text-den-hint text-den-text-muted hover:bg-[var(--den-selection-hover)] hover:text-den-text"
                data-testid="scans-history-sort-dir"
                aria-label={`Sort ${props.runSortDir === "asc" ? "ascending" : "descending"}`}
                onClick={() => props.onRunSortDirToggle()}
              >
                {dirGlyph()}
              </button>
            </div>
            <Show
              when={props.scans.length > 0}
              fallback={
                <p class="m-0 px-1 py-3 text-den-hint text-den-text-muted" role="status">
                  No scan history for this project yet.
                </p>
              }
            >
              <Scrollport
                class="min-h-0 flex-1"
                contentClass="flex flex-col"
                data-testid="scans-history"
                viewport={{
                  id: listboxId,
                  role: "listbox",
                  tabIndex: 0,
                  "aria-label": "Scan runs",
                  get "aria-activedescendant"() {
                    return props.scans[activeRun()]
                      ? `${listboxId}-option-${activeRun()}`
                      : undefined;
                  },
                  onKeyDown: onRunListKeyDown,
                }}
              >
                <For each={props.scans}>
                  {(scan, index) => (
                    <button
                      id={`${listboxId}-option-${index()}`}
                      type="button"
                      class="flex w-full flex-col items-start gap-0.5 rounded-den-sm border-0 bg-transparent p-2 text-left text-den-text hover:bg-[var(--den-selection-hover)]"
                      classList={{
                        "bg-[var(--den-selection)]": props.selectedId === scan.id,
                        "bg-[var(--den-selection-hover)]":
                          activeRun() === index() && props.selectedId !== scan.id,
                        "shadow-[inset_0_0_0_1px_var(--den-accent)]":
                          activeRun() === index() && props.selectedId === scan.id,
                      }}
                      data-testid="scans-run-card"
                      role="option"
                      tabindex={-1}
                      aria-selected={props.selectedId === scan.id}
                      data-run-index={index()}
                      onPointerMove={() => setActiveRun(index())}
                      onClick={() => pickRun(scan.id)}
                    >
                      <span class="max-w-full truncate text-den-body font-[550] tracking-[-0.01em]">
                        {formatScanEngine(scan, props.scannerLabels)}
                      </span>
                      <span class="max-w-full truncate text-den-hint text-den-text-muted tabular-nums">
                        {formatScanStatus(scan.status, scan.long_running)}
                        {" · "}
                        {formatScanTimestamp(
                          scan.created_at,
                        )}
                        {" · "}
                        {scanFindingsCounts(scan)}
                      </span>
                    </button>
                  )}
                </For>
              </Scrollport>
              <TablePager
                testId="scans-history-pager"
                page={props.historyPage}
                shownPage={props.shownHistoryPage}
                itemCount={props.scans.length}
                hasNext={props.hasMore ?? false}
                loading={props.loading}
                onPageChange={props.onHistoryPageChange}
                ariaLabel="Run list pagination"
                class="shrink-0 border-t-0 bg-transparent pt-1.5"
              />
            </Show>
        </AnchoredSurface>
      </Show>
    </div>
  );
}
