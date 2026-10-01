import { For, Show, createSignal } from "solid-js";
import type { FindingLedgerEntry } from "../../api/types.ts";
import {
  advisoryKindLabel,
  formatAdvisory,
  formatLocation,
  formatScannerId,
  hintOverlaySnippet,
  levelChipClass,
  levelLabel,
} from "../../lib/scan-display.ts";
import {
  absenceSentence,
  ignoreSentence,
  ledgerStateChipClass,
  ledgerStateIsAbsent,
  ledgerStateLabel,
} from "../../lib/ledger-display.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { DetailPane } from "../list/DetailPane.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { FindingDataflow } from "./FindingDataflow.tsx";
import type { FindingLedgerModel } from "./create-finding-ledger.ts";

type Props = {
  entry: FindingLedgerEntry;
  ledger: FindingLedgerModel;
  scannerLabels: Readonly<Record<string, string>>;
  repoRoot?: string;
  /** Opens the run that recorded this entry's last event. */
  onOpenRun: (scanId: string) => void;
  /** Prepares finding draft for agent handoff. */
  onFixWithAgent: () => void;
  onAddToChat: () => void;
  addingToChat: boolean;
  onClose: () => void;
};

function absoluteTime(iso?: string): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function withRelative(iso?: string): string {
  const relative = relativeTimeLabel(iso);
  return relative ? `${absoluteTime(iso)} · ${relative}` : absoluteTime(iso);
}

export function LedgerFindingDetail(props: Props) {
  const [hintsOpen, setHintsOpen] = createSignal(false);
  const finding = () => props.entry.finding;
  const fingerprint = () => finding().fingerprints.primary;
  /** Whether the catalog marks this entry withdrawable. */
  const withdrawable = (entryID: string) =>
    (props.ledger.ignores.value()?.rules ?? []).some(
      (rule) => rule.withdrawable && rule.id === entryID,
    );
  const engine = () =>
    formatScannerId(
      finding().tool.driver_id || finding().tool.name || props.entry.scanner_id,
      props.scannerLabels,
    );
  return (
    <DetailPane
      testId="ledger-detail"
      eyebrow={
        <>
          <span class={`den-scans-level ${levelChipClass(finding().level)}`}>
            {levelLabel(finding().level)}
          </span>
          <Show when={advisoryKindLabel(finding().properties?.lycaon?.advisory)}>
            {(label) => <span class="den-scans-advisory-kind">{label()}</span>}
          </Show>
          <span>{engine()}</span>
        </>
      }
      onClose={props.onClose}
    >
      <p class="den-scans-detail-title">{finding().message}</p>

      <div class="den-ledger-detail-actions">
        <DenButton
          variant="secondary"
          compact
          data-testid="ledger-add-to-chat"
          disabled={props.addingToChat}
          onClick={props.onAddToChat}
        >
          Add to chat
        </DenButton>
        <DenButton
          variant="primary"
          compact
          data-testid="ledger-fix-with-agent"
          onClick={props.onFixWithAgent}
        >
          Fix with agent
        </DenButton>
      </div>

      <dl class="den-scans-drill-grid">
        <dt>State</dt>
        <dd>
          <span class={ledgerStateChipClass(props.entry.state)}>
            <span class="den-ledger-state__dot" aria-hidden="true" />
            {ledgerStateLabel(props.entry.state)}
          </span>
        </dd>
        <dt>Rule</dt>
        <dd class="den-scans-row__rule">{finding().rule_id}</dd>
        <dt>Fingerprint</dt>
        <dd class="den-scans-mono">{fingerprint()}</dd>
        <dt>Engine</dt>
        <dd>{engine()}</dd>
        <dt>Advisory</dt>
        <dd>{formatAdvisory(finding().properties?.lycaon?.advisory) || "—"}</dd>
        <dt>Locations</dt>
        <dd>
          <ul>
            <For each={finding().locations ?? []}>
              {(location) => <li>{formatLocation(location, props.repoRoot)}</li>}
            </For>
          </ul>
        </dd>
      </dl>

      <section class="den-ledger-block" data-testid="ledger-detail-history">
        <p class="den-ledger-block__title">History</p>
        <dl class="den-scans-drill-grid">
          <dt>First seen</dt>
          <dd>{withRelative(props.entry.first_seen_at)}</dd>
          <dt>Last seen</dt>
          <dd>{withRelative(props.entry.last_seen_at)}</dd>
          <dt>Observations</dt>
          <dd>{props.entry.observations}</dd>
        </dl>
      </section>

      <Show when={ledgerStateIsAbsent(props.entry.state)}>
        <section class="den-ledger-block" data-testid="ledger-detail-absence">
          <p class="den-ledger-block__title">Why it left</p>
          <p class="den-ledger-absence">
            {absenceSentence(props.entry.state, props.entry.absence)}
          </p>
        </section>
      </Show>

      <Show when={props.entry.ignore} keyed>
        {(ignore) => (
          <section class="den-ledger-block" data-testid="ledger-detail-ignore">
            <p class="den-ledger-block__title">
              {ignore.expired ? "Ignored until recently" : "Ignored"}
            </p>
            <p class="den-ledger-absence">{ignoreSentence(ignore)}</p>
            <Show when={withdrawable(ignore.entry_id)}>
              <DenButton
                variant="secondary"
                compact
                class="self-start"
                disabled={props.ledger.ignorePending()}
                data-testid="ledger-ignore-withdraw"
                onClick={() => void props.ledger.removeIgnore(ignore.entry_id)}
              >
                Withdraw this decision
              </DenButton>
            </Show>
            <Show when={props.ledger.ignoreError()}>
              {(message) => (
                <p class="den-ledger-note den-ledger-note--error" role="alert">
                  {message()}
                </p>
              )}
            </Show>
          </section>
        )}
      </Show>

      <Show keyed when={finding().dataflow}>
        {(flow) => <FindingDataflow flow={flow} repoRoot={props.repoRoot} />}
      </Show>

      <section class="den-ledger-block" data-testid="ledger-detail-evidence">
        <p class="den-ledger-block__title">Evidence</p>
        <Show
          when={props.entry.last_scan_id}
          keyed
          fallback={
            <p class="den-ledger-note">
              The run that recorded this is no longer retained.
            </p>
          }
        >
          {(scanId) => (
            <DenButton
              variant="secondary"
              compact
              class="self-start"
              data-testid="ledger-open-run"
              onClick={() => props.onOpenRun(scanId)}
            >
              Open the run that recorded this
            </DenButton>
          )}
        </Show>
        <DenButton
          variant="secondary"
          compact
          class="self-start"
          data-testid="ledger-hints-toggle"
          onClick={() => setHintsOpen((open) => !open)}
        >
          {hintsOpen() ? "Hide hint overlay" : "Guide the agent on this rule"}
        </DenButton>
        <Show when={hintsOpen()}>
          <p class="den-scans-snippet-label">Hint overlay</p>
          <Scrollport
            class="den-scans-snippet"
            contentAs="pre"
            contentClass="den-scans-snippet__content"
            axis="both"
            data-testid="ledger-hints-snippet"
          >
            {hintOverlaySnippet(finding())}
          </Scrollport>
        </Show>
      </section>
    </DetailPane>
  );
}
