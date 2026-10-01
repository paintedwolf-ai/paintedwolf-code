import { For, Show, createSignal } from "solid-js";
import type {
  FindingLedgerState,
  FindingLevel,
  SecurityScannerState,
} from "../../api/types.ts";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { DenMenuTrigger } from "../primitives/DenMenuTrigger.tsx";
import { createAnchoredPopoverFocus } from "../../platform/interaction/modal-focus-trap.ts";
import { FINDING_LEDGER_STATE_LABELS } from "../../lib/finding-ledger-state.generated.ts";
import { kpiEntries } from "../../lib/scan-display.ts";
import { scannerReadinessLine } from "../../lib/ledger-display.ts";
import { scannerJobLabel } from "../../settings/extensions/scanners-catalog-model.ts";
import type { FindingLedgerModel } from "./create-finding-ledger.ts";

type Props = {
  ledger: FindingLedgerModel;
  scanners: readonly SecurityScannerState[];
  /** States offered inside the current tab. */
  states: readonly FindingLedgerState[];
  disabled?: boolean;
};

/** Filter popover for finding severity, state, and scanner. */
export function FindingFilterChip(props: Props) {
  const [open, setOpen] = createSignal(false);
  let triggerEl: HTMLButtonElement | undefined;
  let panelEl: HTMLDivElement | undefined;
  createAnchoredPopoverFocus(open, () => panelEl, {
    trigger: () => triggerEl,
    onEscape: () => setOpen(false),
  });

  const ledger = () => props.ledger;
  const active = () =>
    ledger().levels().size + ledger().states().size + ledger().scannerIds().size;
  const label = () => (active() > 0 ? `Filter · ${active()}` : "Filter");
  const severities = () => kpiEntries(ledger().byLevel() ?? {});

  return (
    <>
      <DenMenuTrigger
        label={label()}
        open={open()}
        popup="dialog"
        active={active() > 0}
        disabled={props.disabled}
        testId="ledger-filter"
        buttonRef={(element) => {
          triggerEl = element;
        }}
        onClick={() => setOpen((value) => !value)}
      />
      <Show when={open()}>
        <AnchoredSurface
          ref={(element) => {
            panelEl = element;
          }}
          class="den-menu-surface"
          role="dialog"
          ariaLabel="Filter findings"
          anchor={() => triggerEl}
          preferredSide="bottom"
          align="end"
          dismissOnScroll
          onDismiss={() => setOpen(false)}
        >
          <div class="den-finding-filter__body" data-testid="ledger-filter-panel">
            <p class="den-finding-filter__group">Severity</p>
            <For each={severities()}>
              {(row) => (
                <label class="den-finding-filter__row">
                  <DenCheckboxControl
                    checked={ledger().levels().has(row.level as FindingLevel)}
                    data-testid={`ledger-filter-level-${row.level}`}
                    onChange={() => ledger().toggleLevel(row.level as FindingLevel)}
                  />
                  <span class="den-finding-filter__row-text">{row.label}</span>
                  <span class="den-finding-filter__row-count">{row.count}</span>
                </label>
              )}
            </For>

            <p class="den-finding-filter__group">State</p>
            <For each={props.states}>
              {(state) => (
                <label class="den-finding-filter__row">
                  <DenCheckboxControl
                    checked={ledger().states().has(state)}
                    data-testid={`ledger-filter-state-${state}`}
                    onChange={() => ledger().toggleState(state)}
                  />
                  <span class="den-finding-filter__row-text">
                    {FINDING_LEDGER_STATE_LABELS[state]}
                  </span>
                  <span class="den-finding-filter__row-count">
                    {ledger().counts()?.[state] ?? 0}
                  </span>
                </label>
              )}
            </For>
            <Show when={props.scanners.length > 1}>
              <p class="den-finding-filter__group">Scanner</p>
              <For each={props.scanners}>
                {(scanner) => (
                  <label class="den-finding-filter__row den-finding-filter__row--stacked">
                    <DenCheckboxControl
                      checked={
                        ledger().scannerIds().size === 0 ||
                        ledger().scannerIds().has(scanner.id)
                      }
                      data-testid={`ledger-filter-scanner-${scanner.id}`}
                      onChange={() => ledger().toggleScanner(scanner.id)}
                    />
                    <span class="den-finding-filter__row-text">
                      {scannerJobLabel(scanner)}
                      <span class="den-finding-filter__row-meta">
                        {scannerReadinessLine(scanner)}
                      </span>
                    </span>
                  </label>
                )}
              </For>
            </Show>

            <Show when={ledger().filtersActive()}>
              <div class="den-finding-filter__actions">
                <button
                  type="button"
                  class="den-scans-filter-clear"
                  data-testid="ledger-filter-clear"
                  onClick={() => ledger().clearFilters()}
                >
                  Clear filters
                </button>
              </div>
            </Show>
          </div>
        </AnchoredSurface>
      </Show>
    </>
  );
}
