import { For, Show } from "solid-js";
import type { SecurityOverview } from "../../api/types.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { ScanProgressList } from "./ScanProgressList.tsx";
import { scannerReadinessLine } from "../../lib/ledger-display.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { joinScannerLabels, type DisplayedFullPass } from "../../lib/scan-coverage.ts";
import { scannerJobLabel, scannerJobPhrase } from "../../settings/extensions/scanners-catalog-model.ts";

export type EmptyReason = "disabled" | "scanning" | "unscanned" | "changes_only" | "filtered" | "clean";

export function emptyReasonFor(options: {
  enabled: boolean;
  filtersActive: boolean;
  running: boolean;
  fullPassCompleted: boolean;
  /** Some scanner has completed a scan of changed files. */
  changesScanned: boolean;
}): EmptyReason {
  if (!options.enabled) return "disabled";
  if (options.running) return "scanning";
  if (options.filtersActive) return "filtered";
  if (options.fullPassCompleted) return "clean";
  if (options.changesScanned) return "changes_only";
  return "unscanned";
}

/** Summary of last full pass coverage and omitted scanners. */
function cleanPassSentence(overview: SecurityOverview | null): string {
  const full = overview?.last_full;
  const when = relativeTimeLabel(full?.completed_at);
  const finished = when ? `The last full scan finished ${when}` : "The last full scan finished";
  const outcome =
    full?.coverage_status === "complete"
      ? `${finished} and reported nothing. Changes since then are scanned as they are written.`
      : `${finished} and reported nothing, but it did not cover the whole project.`;
  return [outcome, ...passGapSentences(overview)].join(" ");
}

function passGapSentences(overview: SecurityOverview | null): string[] {
  const full = overview?.last_full;
  if (!overview || !full) return [];
  const phraseOf = (id: string) => {
    const scanner = overview.scanners.find((candidate) => candidate.id === id);
    return scanner ? scannerJobPhrase(scanner) : id;
  };
  const opening = (phrases: readonly string[]) => {
    const joined = joinScannerLabels(phrases);
    return joined.charAt(0).toUpperCase() + joined.slice(1);
  };
  const members = new Set(full.members.map((member) => member.scanner_id));
  const notStarted = full.members
    .filter((member) => member.phase === "not_started")
    .map((member) => phraseOf(member.scanner_id));
  const outside = overview.scanners
    .filter((scanner) => scanner.available && !members.has(scanner.id))
    .map((scanner) => scannerJobPhrase(scanner));
  const out: string[] = [];
  if (notStarted.length > 0) out.push(`${opening(notStarted)} did not start.`);
  if (outside.length > 0) {
    out.push(`${opening(outside)} ${outside.length === 1 ? "was" : "were"} not part of it.`);
  }
  return out;
}

type Props = {
  reason: EmptyReason;
  overview: SecurityOverview | null;
  /** The pass on screen; a finished one stays for the minimum visible interval. */
  displayed: DisplayedFullPass | null;
  starting: boolean;
  onStartFullScan: () => void;
  onClearFilters: () => void;
};

export function SecurityEmptyState(props: Props) {
  const available = () => (props.overview?.scanners ?? []).filter((scanner) => scanner.available);
  const finished = () => props.displayed?.live === false;
  const asksForFullPass = () => props.reason === "unscanned" || props.reason === "changes_only";

  const scannerRows = () => (
    <dl class="den-scans-scanner-rows" data-testid="scans-empty-scanners">
      <For each={props.overview?.scanners ?? []}>
        {(scanner) => (
          <>
            <dt classList={{ "den-scans-scanner-rows__off": !scanner.available }}>
              {scannerJobLabel(scanner)}
            </dt>
            <dd>{scannerReadinessLine(scanner)}</dd>
          </>
        )}
      </For>
    </dl>
  );

  return (
    <div class="den-scans-empty-state" data-testid="scans-empty" data-reason={props.reason}>
      <Show when={props.reason === "disabled"}>
        <p class="den-scans-empty-state__title">Security scanning is off</p>
        <p class="den-scans-empty-state__body">
          Turn it on in Settings, under Security scanners, to record what this project
          holds.
        </p>
      </Show>

      <Show when={props.reason === "filtered"}>
        <p class="den-scans-empty-state__title">No findings match this filter</p>
        <p class="den-scans-empty-state__body">
          The project's totals above are unchanged — this view is narrowed, not empty.
        </p>
        <DenButton
          variant="secondary"
          data-testid="scans-empty-clear"
          onClick={() => props.onClearFilters()}
        >
          Clear filter
        </DenButton>
      </Show>

      <Show when={props.reason === "scanning"}>
        <p class="den-scans-empty-state__title">
          {finished() ? "Full scan finished" : "Scanning this project"}
        </p>
        <p class="den-scans-empty-state__body">
          {finished()
            ? "The last scanner in this pass just finished."
            : "Findings appear as each scanner finishes. You can leave this stage."}
        </p>
        <ShowLatest when={props.displayed} by={(displayed) => displayed.pass.assessment_id}>
          {(displayed) => (
            <div class="den-scans-empty-state__detail">
              <ScanProgressList
                pass={displayed().pass}
                overview={props.overview}
                testId="scans-empty-progress"
              />
            </div>
          )}
        </ShowLatest>
      </Show>

      <Show when={props.reason === "clean"}>
        <p class="den-scans-empty-state__title">No findings reported</p>
        <p class="den-scans-empty-state__body">{cleanPassSentence(props.overview)}</p>
        <DenButton
          variant="secondary"
          data-testid="scans-empty-start"
          disabled={props.starting || available().length === 0}
          onClick={() => props.onStartFullScan()}
        >
          {props.starting ? "Starting…" : "Run full scan again"}
        </DenButton>
        <div class="den-scans-empty-state__detail">{scannerRows()}</div>
      </Show>

      <Show when={asksForFullPass()}>
        <p class="den-scans-empty-state__title">
          {props.reason === "changes_only" ? "No full scan yet" : "Nothing scanned yet"}
        </p>
        <p class="den-scans-empty-state__body">
          {props.reason === "changes_only"
            ? "Changed files have been scanned as they were written, and none of those scans reported anything. "
            : "Scanning follows your changes: every admitted write is scanned as a delta, and nothing else runs on its own. "}
          A full pass records what this project already holds, so later changes have
          something to be measured against.
        </p>
        <DenButton
          variant="primary"
          data-testid="scans-empty-start"
          disabled={props.starting || available().length === 0}
          onClick={() => props.onStartFullScan()}
        >
          {props.starting ? "Starting…" : "Run full scan"}
        </DenButton>
        <Show
          when={available().length > 0}
          fallback={
            <p class="den-scans-empty-state__note">
              No scanner is available for this project. Settings, under Security scanners,
              says why.
            </p>
          }
        >
          <div class="den-scans-empty-state__detail">
            <p class="den-scans-empty-state__detail-title">
              {available().length === 1 ? "1 scanner will run" : `${available().length} scanners will run`}
            </p>
            {scannerRows()}
          </div>
        </Show>
      </Show>
    </div>
  );
}
