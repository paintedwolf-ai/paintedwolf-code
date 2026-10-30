import { Index, Show, createEffect, createMemo, createResource, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { commandActivityFacts } from "../../chat/tool/command-activity-details.ts";
import { activityExecutions } from "../../chat/tool/command-activity-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { CommandActivitySources } from "./CommandActivitySources.tsx";
import { TurnLoadFootLine } from "./TurnLoadFootLine.tsx";
import { ToolPartShell } from "./ToolPartShell.tsx";
import { ToolContentLink } from "./ToolContentLink.tsx";
import { useToolContent } from "../../chat/tool/tool-content.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  rootRefs?: readonly ResolveProjectRoot[];
};

const CALL_PAGE = 2;
const PREVIEW_CHARS = 480;

export function CommandActivityCard(props: Props) {
  return <ToolPartShell {...props}><CommandActivityDetails {...props} /><TurnLoadFootLine part={props.part} /></ToolPartShell>;
}

function CommandActivityDetails(props: Props) {
  const activity = () => props.part.commandActivity!;
  const content = useToolContent();
  const [callPage, setCallPage] = createSignal(0);
  const [receiptCursors, setReceiptCursors] = createSignal<string[]>([]);
  createEffect(() => {
    const target = content?.revealedCallId?.();
    if (!target) return;
    const index = activity().calls.findIndex(call => call.toolCallId === target);
    if (index >= 0) setCallPage(Math.floor(index / CALL_PAGE));
  });
  const receiptCursor = () => receiptCursors().at(-1) ?? activity().continuation?.cursor;
  const calls = createMemo(() => activity().calls.slice(callPage() * CALL_PAGE, (callPage() + 1) * CALL_PAGE));
  const snapshot = createMemo(() => {
    const client = props.client ?? getLycaonClient();
    if (!client || !props.sessionId) return undefined;
    const requests = [...new Map(calls().flatMap(call => call.commandProgram ? [[call.commandProgram.id, call.commandProgram] as const] : [])).values()];
    const cursor = receiptCursor();
    const limit = activity().continuation ? 6 : 32;
    const key = JSON.stringify([requests.map(program => [program.id, program.state, program.settled_at]), activity().fact.state, activity().members.map(member => member.execution_ids), cursor, limit]);
    return { client, sessionId: props.sessionId, requests, cursor, limit, key };
  }, undefined, { equals: (a,b) => a?.client === b?.client && a?.sessionId === b?.sessionId && a?.key === b?.key });
  const [pages] = createResource(snapshot, async ({ client, sessionId, requests, cursor, limit }) =>
    Promise.all(requests.map(program => client.getCommandProgramReceipt(sessionId, program.id, { limit, cursor }))),
  );
  const loaded = () => pages.state === "ready" || pages.state === "refreshing" ? pages.latest ?? [] : [];
  const executions = createMemo(() => {
    const receipts = loaded().flatMap(page => page?.executions ?? []);
    return activity().continuation ? receipts : activityExecutions(activity(), receipts);
  });
  const facts = createMemo(() => {
    const result = commandActivityFacts(activity(), executions(), props.part.feedback);
    const stop = loaded()[0]?.program.stop_code;
    if (activity().fact.execution_ids.length === 0 && stop && !result.some(fact => fact.value === stop)) result.push({ label: "Code", value: stop });
    const notStarted = loaded().flatMap(page => page.nodes).filter(node => node.state === "not_started");
    if (notStarted.length) result.splice(1, 0, { label: "Not started", value: `${notStarted.length} recorded statements` });
    return result;
  });
  const previews = createMemo(() => executions().flatMap(execution => (execution.outputs ?? []).map(output => ({ ...output, execution }))));
  return <div class="den-tool-part-card-structured den-command-activity-details" data-testid="command-activity-details">
    <section class="den-tool-part-card-section" aria-label="Outcome">
      <dl class="den-tool-part-card-facts"><Index each={facts().slice(0, 8)}>{fact => <div><dt>{fact().label}</dt><dd data-tip={fact().value} data-tip-when-clipped="">{fact().value}</dd></div>}</Index></dl>
      <Show when={facts().length > 8}><p class="den-tool-part-card-note">Additional facts are available in execution receipts.</p></Show>
    </section>
    <Show when={executions().length > 0}>
      <section class="den-tool-part-card-section" aria-label="Execution">
        <h4 class="den-tool-part-card-section-label">Execution</h4>
        <KeyedIndex each={executions().slice(0, 6)} keyOf={execution => execution.id}>{execution => <div class="den-command-activity-step"><code>{execution().command.slice(0, 192)}{execution().command.length > 192 ? "…" : ""}</code><span>{execution().state}{execution().status_code !== undefined ? ` · exit ${execution().status_code}` : ""}</span></div>}</KeyedIndex>
        <Show when={executions().length > 6}><p class="den-tool-part-card-note">{executions().length} steps in this activity</p></Show>
        <Show when={activity().continuation}><div class="den-command-activity-paging" role="group" aria-label="Execution pages">
          <button type="button" class="den-chat-content-link" disabled={receiptCursors().length === 0} onClick={() => setReceiptCursors(value => value.slice(0,-1))}>Previous operations</button>
          <button type="button" class="den-chat-content-link" disabled={!loaded()[0]?.executions_omitted} onClick={() => { const next = loaded()[0]?.next_cursor; if (next) setReceiptCursors(value => [...value,next]); }}>Next operations</button>
        </div></Show>
        <ToolContentLink pane={`activity:${activity().fact.id}:receipts`} label="Execution receipts" content={{ kind: "inline", text: JSON.stringify({ program: loaded()[0]?.program, activities: activity().members, facts: facts(), executions: executions(), nodes: loaded().flatMap(page => page.nodes), feedback: props.part.feedback }, null, 2) }} projectId={props.projectId} />
      </section>
    </Show>
    <Show when={previews().length > 0}>
      <section class="den-tool-part-card-section" aria-label="Output">
        <h4 class="den-tool-part-card-section-label">Output preview</h4>
        <KeyedIndex each={previews().slice(-1)} keyOf={output => `${output.execution.id}:${output.stream}`}>{output => <div>
          <p class="den-tool-part-card-note">{output().execution.command.slice(0,96)} · {output().stream}</p>
          <pre class="den-command-activity-output-preview">{output().preview.slice(0, PREVIEW_CHARS)}</pre>
          <Show when={!output().complete}><p class="den-tool-part-card-note">Incomplete {output().stream} stream</p></Show>
          <ToolContentLink pane={`activity:${activity().fact.id}:${output().execution.id}:${output().stream}`} label={`${output().stream} preview`} content={{ kind: "inline", text: output().preview }} projectId={props.projectId} />
          <Show when={output().preview_truncated || output().preview.length > PREVIEW_CHARS}><p class="den-tool-part-card-note">Preview is limited; retained reference: {output().ref}</p></Show>
        </div>}</KeyedIndex>
      </section>
    </Show>
    <CommandActivitySources activity={activity()} calls={calls()} offset={callPage() * CALL_PAGE} projectId={props.projectId}
      onPrevious={() => setCallPage(value => value - 1)} onNext={() => setCallPage(value => value + 1)} />
    <Show when={pages.state === "errored"}><p class="den-tool-part-card-note">Execution details are unavailable. The recorded activity is still shown.</p></Show>
  </div>;
}
