import { For, Show, createEffect, createSignal, on } from "solid-js";
import type { SecurityOverview } from "../../api/types.ts";
import {
  coverageChipLabel,
  coverageChipState,
  coverageDotState,
  coverageSentence,
  securityBaselineTime,
  defaultFullScanSelection,
  fullScanToggleLabel,
  type DisplayedFullPass,
} from "../../lib/scan-coverage.ts";
import { scannerReadinessLine } from "../../lib/ledger-display.ts";
import { scannerJobLabel } from "../../settings/extensions/scanners-catalog-model.ts";
import { ScanProgressList } from "./ScanProgressList.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { StableLabel, widestDigits } from "../primitives/StableLabel.tsx";

type Props = {
  projectId: string | undefined;
  overview: SecurityOverview | null;
  /** The pass on screen, which outlives the host's running pass by the minimum visible interval. */
  displayed: DisplayedFullPass | null;
  newSince: boolean;
  /** Whether rule or engine changes invalidate a scanner's last full pass. */
  passSuperseded: boolean;
  onToggleNewSince: () => void;
  /** The pane presents sidecar request errors. */
  onStart: (scannerIds: string[]) => Promise<void>;
  starting: boolean;
};

export function SecurityCoverage(props: Props) {
  const [open, setOpen] = createSignal(false);
  const [selected, setSelected] = createSignal(new Set<string>());
  createEffect(on(() => props.projectId, () => {
    setOpen(false);
    setSelected(new Set<string>());
  }));
  const chipState = () => coverageChipState(props.overview);
  const running = () => props.overview?.running ?? null;

  const toggle = () => {
    if (!open()) setSelected(defaultFullScanSelection(props.overview));
    setOpen((v) => !v);
  };

  const setChecked = (id: string, on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const startDisabled = () => props.starting || !props.overview || running() != null || selected().size === 0;
  const pass = () => props.displayed?.pass ?? props.overview?.last_full ?? null;
  // Reserve label widths to prevent control resizing.
  const toggleReserve = () => {
    const members = Math.max(
      props.displayed?.pass.members.length ?? 0,
      props.overview?.scanners.length ?? 0,
    );
    const digits = widestDigits(members);
    return ["Run full scan", "Full scan finished", `Full scan · ${digits} of ${digits} done`];
  };

  return (
    <div class="den-scans-coverage" data-testid="scans-coverage">
      <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
        <p class="den-scans-coverage__line" data-testid="scans-coverage-line">
          {coverageSentence(props.overview)}
        </p>
        <div class="den-browse-filter-toggles shrink-0">
          <button
            type="button"
            class="den-browse-filter-toggle"
            classList={{ "den-browse-filter-toggle--on": props.newSince }}
            data-testid="scans-new-since-chip"
            aria-pressed={props.newSince}
            disabled={!securityBaselineTime(props.overview)}
            onClick={() => props.onToggleNewSince()}
          >
            New since
          </button>
          <Show when={props.overview}>
            <span
              class="den-scans-coverage-chip"
              data-state={coverageDotState(chipState())}
              data-coverage={chipState()}
              data-testid="scans-coverage-chip"
            >
              <span class="den-scans-coverage-chip__dot" aria-hidden="true" />
              {coverageChipLabel(chipState())}
            </span>
          </Show>
        </div>
        <DenButton
          variant={props.displayed ? "secondary" : "primary"}
          compact
          class="den-scans-full-scan-toggle shrink-0"
          data-testid="scans-full-scan-toggle"
          data-pass={props.displayed ? (props.displayed.live ? "running" : "finished") : undefined}
          aria-expanded={open()}
          aria-controls="scans-full-scan-panel"
          disabled={!props.overview}
          onClick={toggle}
        >
          <StableLabel
            label={fullScanToggleLabel(props.displayed)}
            reserve={toggleReserve()}
            align="center"
            testId="scans-full-scan-toggle-label"
          />
        </DenButton>
      </div>
      <Show when={props.passSuperseded}>
        <p class="den-scans-coverage__note" data-testid="scans-pass-stale" role="status">
          A scanner's rules or engine changed since its last full pass, so what that pass
          established no longer applies. The next full pass restores it.
        </p>
      </Show>
      <Show when={open()}>
        <div
          id="scans-full-scan-panel"
          class="den-scans-full-panel"
          data-testid="scans-full-scan-panel"
        >
          <Show when={!running()}>
            {
              <ul class="m-0 flex list-none flex-col gap-2 p-0" data-testid="scans-full-scan-scanners">
                <For each={props.overview?.scanners ?? []}>
                  {(scanner) => (
                    <li>
                      <label class="den-choice-label w-full items-start">
                        <DenCheckboxControl
                          data-testid={`scans-full-scan-scanner-${scanner.id}`}
                          checked={selected().has(scanner.id)}
                          disabled={!scanner.available}
                          onChange={(event) => setChecked(scanner.id, event.currentTarget.checked)}
                        />
                        <span class="flex min-w-0 flex-1 flex-col gap-[3px]">
                          <span class="den-scans-full-panel__scanner-label">{scannerJobLabel(scanner)}</span>
                          <span class="den-scans-full-panel__scanner-note">
                            {scannerReadinessLine(scanner)}
                          </span>
                        </span>
                      </label>
                    </li>
                  )}
                </For>
              </ul>
            }
          </Show>
          <ShowLatest when={pass()} by={(pass) => pass.assessment_id}>
            {(pass) => (
              <Show when={pass().members.length > 0}>
                <p class="den-scans-full-panel__scanner-label">
                  {props.displayed?.live ? "Current full scan" : "Last full scan"}
                </p>
                <ScanProgressList pass={pass()} overview={props.overview} />
              </Show>
            )}
          </ShowLatest>
          <div class="flex items-center gap-2">
            <Show when={!running()}>
              <DenButton
                variant="primary"
                compact
                data-testid="scans-full-scan-start"
                disabled={startDisabled()}
                onClick={() => void props.onStart([...selected()])}
              >
                Start
              </DenButton>
            </Show>
            <DenButton
              variant="ghost"
              compact
              data-testid="scans-full-scan-close"
              onClick={() => setOpen(false)}
            >
              Close
            </DenButton>
          </div>
        </div>
      </Show>
    </div>
  );
}
