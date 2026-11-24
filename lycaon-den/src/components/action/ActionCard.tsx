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
import { useNetworkActions } from "../../chat/network-action-context.ts";
import type { ToolBriefingSlots } from "../tool/tool-briefing-slots.ts";
import { ToolPartShell } from "../tool/ToolPartShell.tsx";
import { ActionLinks } from "./ActionLinks.tsx";
import { liveToolProcess } from "../../chat/tool/tool-part-model.ts";
import { ActionProcess } from "./ActionProcess.tsx";
import { visualFromPart } from "../../chat/visual/visual-artifact-model.ts";
import { visualProducerEntryKey } from "../../chat/visual/visual-artifact-canonical.ts";
import { TranscriptVisualSlot } from "../transcript/TranscriptVisualSlot.tsx";
import { ActionPreview } from "./ActionPreview.tsx";
import { activityCoverageNotices, useActionCoverage } from "../../chat/action/action-source-coverage.ts";

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

/** Every action's card: what ran, its summary, facts, preview, latest capture and records. */
export function ActionCard(props: Props) {
  const action = useActionCoverage(() => props.action, props);
  const status = () => (props.action.status === "done" ? "completed" : props.action.status);
  const members = () => [props.part, ...(props.part.foldMembers ?? [])];
  // Hosts the host let through, which the network fact offers to block.
  const allowedHosts = () => new Set(members().flatMap((member) =>
    (member.externalAccess?.endpoints ?? []).filter((endpoint) => endpoint.decision === "allow").map((endpoint) => endpoint.host)));
  const process = () => {
    const sessionId = props.sessionId;
    const handle = sessionId ? members().map(liveToolProcess).find((live) => live !== null) : undefined;
    return sessionId && handle ? { sessionId, handle } : undefined;
  };
  // A live process states its own state, so the card drops the recorded one.
  const facts = () => (process() ? action().facts.filter((fact) => fact.kind !== "process") : action().facts);
  const port = () => props.action.facts.find((fact) => fact.kind === "process")?.port;
  // A fold keeps only its newest capture; the captured fact names it.
  const capture = () => {
    for (const member of [...members()].reverse()) {
      const artifact = visualFromPart(member);
      if (artifact) return { artifact, output: member.output };
    }
    return undefined;
  };
  return (
    <ToolPartShell
      part={{ ...props.part, title: props.action.ran.text }}
      layout={props.layout}
      sessionId={props.sessionId}
      projectId={props.projectId}
      client={props.client}
      name={props.action.label}
      outcome={formatActionRow(action())}
      status={status()}
      testId="action-card"
    >
      <div class="den-action-card" data-testid="action-card-body">
        <ActionRanRegion ran={props.action.ran} more={props.action.more} />
        {props.slots?.summary}
        <ActionFacts facts={facts()} allowedHosts={allowedHosts()} />
        <Show when={process()} keyed>
          {(live) => <ActionProcess sessionId={live.sessionId} processId={live.handle.handle} initialRunning={live.handle.running} terminationReason={live.handle.termination_reason} port={port()} />}
        </Show>
        <Show when={props.action.preview} keyed>
          {(preview) => <ActionPreview preview={preview} action={props.action} part={props.part} sessionId={props.sessionId} client={props.client} />}
        </Show>
        <Show when={capture()} keyed>
          {(shown) => (
            <TranscriptVisualSlot artifact={shown.artifact} sessionId={props.sessionId} projectId={props.projectId} client={props.client}
              entryKey={visualProducerEntryKey(shown.artifact.id)} toolOutput={shown.output} />
          )}
        </Show>
        <ActionLinks action={props.action} part={props.part} projectId={props.projectId} sessionId={props.sessionId} rootRefs={props.rootRefs} evidence={props.slots?.evidence} />
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

function ActionFacts(props: { facts: readonly ActionFact[]; allowedHosts: ReadonlySet<string> }) {
  return (
    <Show when={props.facts.length}>
      <dl class="den-action-card__facts" data-testid="action-facts">
        <For each={props.facts}>
          {(fact) => (
            <div data-kind={fact.kind} data-tone={dangerous(fact) ? "danger" : undefined}>
              <dt>{ACTION_FACT_LABELS[fact.kind]}</dt>
              <dd>
                <Show when={fact.kind === "source_observation"} fallback={formatFact(fact)}>
                  <ul class="den-action-card__observation">
                    <For each={activityCoverageNotices(fact.source_coverage)}>{(notice) => <li>{notice}</li>}</For>
                  </ul>
                </Show>
                <Show when={fact.kind === "network"}>
                  <BlockHosts hosts={(fact.hosts ?? []).filter((host) => props.allowedHosts.has(host))} />
                </Show>
              </dd>
            </div>
          )}
        </For>
      </dl>
    </Show>
  );
}

/** Blocks a host the call reached for every later call, through a global deny rule. */
function BlockHosts(props: { hosts: readonly string[] }) {
  const actions = useNetworkActions();
  const [blocking, setBlocking] = createSignal<string | null>(null);
  const block = async (host: string) => {
    if (!actions) return;
    setBlocking(host);
    try {
      await actions.blockHost(host);
    } finally {
      setBlocking(null);
    }
  };
  return (
    <Show when={actions}>
      <For each={props.hosts}>
        {(host) => (
          <Show when={!actions?.isBlocked(host)} fallback={<span class="den-action-card__blocked" data-testid="action-host-blocked">{host} blocked</span>}>
            <button type="button" class="den-chat-content-link den-action-card__block" data-testid="action-block-host"
              disabled={blocking() === host} onClick={() => void block(host)}>
              Block {host}
            </button>
          </Show>
        )}
      </For>
    </Show>
  );
}

function dangerous(fact: ActionFact): boolean {
  if (fact.kind === "result") return fact.outcome === "failed" || fact.outcome === "refused" || fact.outcome === "interrupted" || fact.outcome === "fail";
  if (fact.kind === "steps") return (fact.failed ?? 0) > 0;
  if (fact.kind === "found") return (fact.high ?? 0) > 0;
  return fact.kind === "stopped_by" && fact.by === "host_stop";
}
