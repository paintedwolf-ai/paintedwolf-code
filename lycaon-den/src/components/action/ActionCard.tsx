import { For, Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { ActionFact, ActionRan } from "../../api/types.ts";
import { ACTION_FACT_LABELS, formatActionRow, formatFact } from "../../chat/action/action-format.ts";
import type { CardAction } from "../../chat/action/action-merge.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { RedactedText } from "../transcript/RedactionMark.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { NetworkChicklet } from "../tool/NetworkChicklet.tsx";
import { RedactionChicklet } from "../tool/RedactionChicklet.tsx";
import type { ToolBriefingSlots } from "../tool/tool-briefing-slots.ts";
import { ToolPartShell } from "../tool/ToolPartShell.tsx";
import { ActionLinks } from "./ActionLinks.tsx";
import { TurnLoadFootLine } from "../tool/TurnLoadFootLine.tsx";
import { liveToolProcess } from "../../chat/tool/tool-part-model.ts";
import { ActionProcess } from "./ActionProcess.tsx";
import { visualFromPart } from "../../chat/visual/visual-artifact-model.ts";
import { visualProducerEntryKey } from "../../chat/visual/visual-artifact-canonical.ts";
import { TranscriptVisualSlot } from "../transcript/TranscriptVisualSlot.tsx";
import { ActionPreview } from "./ActionPreview.tsx";

const RAN_FIELD = "ran.text";

type Props = {
  part: ToolPartView;
  action: CardAction;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  rootRefs?: readonly ResolveProjectRoot[];
  slots?: ToolBriefingSlots;
};

/** Every action's card: what ran, its summary, facts, preview and records, then how the local AI bore on the call. */
export function ActionCard(props: Props) {
  const status = () => (props.action.status === "done" ? "completed" : props.action.status);
  // A fold's members share one card, so it shows the first member that has each notice.
  const members = () => [props.part, ...(props.part.foldMembers ?? [])];
  const externalAccess = () => members().find((member) => member.externalAccess)?.externalAccess ?? undefined;
  const redaction = () => members().find((member) => member.redaction?.spans?.length)?.redaction ?? undefined;
  const process = () => (props.sessionId ? members().map(liveToolProcess).find((handle) => handle !== null) ?? undefined : undefined);
  // A live process states its own state, so the card drops the recorded one.
  const facts = () => (process() ? props.action.facts.filter((fact) => fact.kind !== "process") : props.action.facts);
  const port = () => props.action.facts.find((fact) => fact.kind === "process")?.port;
  const captures = () => members().flatMap((member) => {
    const artifact = visualFromPart(member);
    return artifact ? [{ artifact, output: member.output }] : [];
  });
  return (
    <ToolPartShell
      part={{ ...props.part, title: props.action.ran.text }}
      layout={props.layout}
      sessionId={props.sessionId}
      projectId={props.projectId}
      client={props.client}
      name={props.action.label}
      outcome={formatActionRow(props.action)}
      status={status()}
      testId="action-card"
    >
      <div class="den-action-card" data-testid="action-card-body">
        <ActionRanRegion ran={props.action.ran} more={props.action.more} />
        {props.slots?.summary}
        <ActionFacts facts={facts()} />
        <Show when={process()} keyed>
          {(live) => <ActionProcess sessionId={props.sessionId!} processId={live.handle} initialRunning={live.running} terminationReason={live.termination_reason} port={port()} />}
        </Show>
        <Show when={externalAccess()} keyed>
          {(access) => <NetworkChicklet externalAccess={access} sessionId={props.sessionId} disclosureKey={transcriptDisclosureKey.toolNetwork(props.part.id)} />}
        </Show>
        <Show when={props.action.preview} keyed>
          {(preview) => <ActionPreview preview={preview} action={props.action} part={props.part} sessionId={props.sessionId} client={props.client} />}
        </Show>
        <For each={captures()}>
          {(capture) => (
            <TranscriptVisualSlot artifact={capture.artifact} sessionId={props.sessionId} projectId={props.projectId} client={props.client}
              entryKey={visualProducerEntryKey(capture.artifact.id)} toolOutput={capture.output} />
          )}
        </For>
        <Show when={redaction()} keyed>
          {(meta) => <p class="den-action-card__notice" data-testid="action-redaction"><RedactionChicklet message={{ host_secret_redaction: meta }} /></p>}
        </Show>
        <ActionLinks action={props.action} part={props.part} projectId={props.projectId} rootRefs={props.rootRefs} evidence={props.slots?.evidence} />
        <TurnLoadFootLine part={props.part} />
      </div>
    </ToolPartShell>
  );
}

function ActionRanRegion(props: { ran: ActionRan; more?: number }) {
  const [copied, setCopied] = createSignal("");
  const copy = async () => {
    try {
      await writeClipboardText(props.ran.text);
      setCopied("Copied");
    } catch {
      setCopied(`Could not copy ${props.ran.noun}`);
    }
  };
  return (
    <div class="den-action-card__ran" data-testid="action-ran">
      <code class="den-action-card__ran-text">
        <Show when={props.ran.redaction} keyed fallback={props.ran.text}>
          {(redaction) => <RedactedText message={{ host_secret_redaction: redaction }} field={RAN_FIELD} text={props.ran.text} />}
        </Show>
      </code>
      <Show when={props.more}>{(more) => <span class="den-action-card__more">and {more().toLocaleString()} more</span>}</Show>
      <button type="button" class="den-action-card__copy den-inset-icon-btn" data-testid="action-ran-copy"
        aria-label={copied() === "Copied" ? "Copied" : `Copy ${props.ran.noun}`} onClick={() => void copy()}>
        <ThemeIcon slot={copied() === "Copied" ? "check" : "copy"} size={13} />
      </button>
      <p class="den-visually-hidden" role="status">{copied()}</p>
    </div>
  );
}

function ActionFacts(props: { facts: readonly ActionFact[] }) {
  return (
    <Show when={props.facts.length}>
      <dl class="den-action-card__facts" data-testid="action-facts">
        <For each={props.facts}>
          {(fact) => (
            <div data-kind={fact.kind} data-tone={dangerous(fact) ? "danger" : undefined}>
              <dt>{ACTION_FACT_LABELS[fact.kind]}</dt>
              <dd>{formatFact(fact)}</dd>
            </div>
          )}
        </For>
      </dl>
    </Show>
  );
}

function dangerous(fact: ActionFact): boolean {
  if (fact.kind === "result") return fact.outcome === "failed" || fact.outcome === "refused" || fact.outcome === "interrupted" || fact.outcome === "fail";
  if (fact.kind === "steps") return (fact.failed ?? 0) > 0;
  if (fact.kind === "found") return (fact.high ?? 0) > 0;
  return fact.kind === "stopped_by" && fact.by === "host_stop";
}
