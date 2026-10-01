import { For, Show, createSignal } from "solid-js";
import type { CodeScan, SecurityFinding } from "../../api/types.ts";
import {
  advisoryKindLabel,
  formatAdvisory,
  formatLocation,
  formatScannerId,
  guidanceForFinding,
  hintOverlaySnippet,
  levelChipClass,
  levelLabel,
} from "../../lib/scan-display.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { DetailPane } from "../list/DetailPane.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { FindingDataflow } from "./FindingDataflow.tsx";

type Props = {
  finding: SecurityFinding;
  scan: CodeScan | null;
  scannerLabels: Readonly<Record<string, string>>;
  repoRoot?: string;
  onAddToChat: () => void;
  addingToChat: boolean;
  onClose: () => void;
};

export function RunFindingDetail(props: Props) {
  const [hintsOpen, setHintsOpen] = createSignal(false);
  const guidance = () => guidanceForFinding(props.finding, props.scan?.guidance);
  const engine = () =>
    formatScannerId(
      props.finding.tool.driver_id || props.finding.tool.name,
      props.scannerLabels,
    );

  return (
    <DetailPane
      testId="scans-drill-down"
      eyebrow={
        <>
          <span class={`den-scans-level ${levelChipClass(props.finding.level)}`}>
            {levelLabel(props.finding.level)}
          </span>
          <Show when={advisoryKindLabel(props.finding.properties?.lycaon?.advisory)}>
            {(label) => (
              <span class="den-scans-advisory-kind" data-testid="scans-advisory-kind">
                {label()}
              </span>
            )}
          </Show>
          <span>{engine()}</span>
        </>
      }
      onClose={() => {
        setHintsOpen(false);
        props.onClose();
      }}
    >
      <p class="den-scans-detail-title">{props.finding.message}</p>
      <div class="den-ledger-detail-actions">
        <DenButton
          variant="secondary"
          compact
          data-testid="scans-add-to-chat"
          disabled={props.addingToChat}
          onClick={props.onAddToChat}
        >
          Add to chat
        </DenButton>
      </div>
      <dl class="den-scans-drill-grid">
        <dt>Rule</dt>
        <dd class="den-scans-row__rule">{props.finding.rule_id}</dd>
        <dt>Fingerprint</dt>
        <dd class="den-scans-mono">{props.finding.fingerprints.primary}</dd>
        <dt>Engine</dt>
        <dd>{engine()}</dd>
        <dt>Advisory</dt>
        <dd>{formatAdvisory(props.finding.properties?.lycaon?.advisory) || "—"}</dd>
        <dt>Locations</dt>
        <dd>
          <ul>
            <For each={props.finding.locations ?? []}>
              {(location) => <li>{formatLocation(location, props.repoRoot)}</li>}
            </For>
          </ul>
        </dd>
        <Show when={relativeTimeLabel(props.finding.history?.introduced_at)}>
          {(introduced) => (
            <>
              <dt>Introduced</dt>
              <dd data-testid="scans-detail-introduced">{introduced()}</dd>
            </>
          )}
        </Show>
        <Show when={guidance()?.fix}>
          <dt>Fix</dt>
          <dd>{guidance()?.fix}</dd>
        </Show>
      </dl>
      <Show keyed when={props.finding.dataflow}>
        {(flow) => <FindingDataflow flow={flow} repoRoot={props.repoRoot} />}
      </Show>
      <DenButton
        variant="secondary"
        compact
        data-testid="scans-hints-toggle"
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
          data-testid="scans-hints-snippet"
        >
          {hintOverlaySnippet(props.finding)}
        </Scrollport>
      </Show>
    </DetailPane>
  );
}
