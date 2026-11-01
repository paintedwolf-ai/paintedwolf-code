import { Index, Show, createEffect, createMemo, createResource, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { commandActivityFacts } from "../../chat/tool/command-activity-details.ts";
import { commandActivityStepOutcome } from "../../chat/tool/command-activity-details.ts";
import { turnLoadRowSummary } from "../../chat/turnload/turn-load-rows.ts";
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
  const [stepPage, setStepPage] = createSignal(0);
  const [wholeCommand, setWholeCommand] = createSignal(false);
  const [receiptCursors, setReceiptCursors] = createSignal<string[]>([]);
  createEffect(() => {
    const target = content?.revealedCallId?.();
    if (!target) return;
    const index = activity().calls.findIndex(call => call.toolCallId === target);
    if (index >= 0) setCallPage(Math.floor(index / CALL_PAGE));
  });
  const receiptCursor = () => receiptCursors().at(-1);
  const calls = createMemo(() => activity().calls.slice(callPage() * CALL_PAGE, (callPage() + 1) * CALL_PAGE));
  const snapshot = createMemo(() => {
    const client = props.client ?? getLycaonClient();
    if (!client || !props.sessionId) return undefined;
    const requests = [...new Map(calls().flatMap(call => call.commandProgram ? [[call.commandProgram.id, call.commandProgram] as const] : [])).values()];
    const cursor = receiptCursor();
    const limit = 6;
    const activityId = !wholeCommand() && activity().fact.execution_ids.length ? activity().fact.id : undefined;
    const key = JSON.stringify([requests.map(program => [program.id, program.state, program.settled_at]), activity().fact.state, activity().fact.execution_count, activity().fact.failed_count, activityId, cursor, limit]);
    return { client, sessionId: props.sessionId, requests, cursor, limit, activityId, key };
  }, undefined, { equals: (a,b) => a?.client === b?.client && a?.sessionId === b?.sessionId && a?.key === b?.key });
  const [pages] = createResource(snapshot, async ({ client, sessionId, requests, cursor, limit, activityId }) =>
    Promise.all(requests.map(program => client.getCommandProgramReceipt(sessionId, program.id, { limit, cursor, activity_id: activityId }))),
  );
  const loaded = () => pages.state === "ready" || pages.state === "refreshing" ? pages.latest ?? [] : [];
  const executions = createMemo(() => {
    const receipts = loaded().flatMap(page => page?.executions ?? []);
    return receipts;
  });
  const facts = createMemo(() => {
    const feedback = activity().fact.execution_ids.length === 0 ? props.part.feedback : props.part.feedback?.filter(entry => executions().some(row => row.stop_code === entry.code || row.host_stop_code === entry.code));
    const result = commandActivityFacts(activity(), wholeCommand() ? [] : executions(), feedback);
    const stop = loaded()[0]?.program.stop_code;
    if (activity().fact.execution_ids.length === 0 && stop && !result.some(fact => fact.value === stop)) result.push({ label: "Code", value: stop });
    return result;
  });
  const previews = createMemo(() => executions().flatMap(execution => (execution.outputs ?? []).map(output => ({ ...output, execution }))));
  return <div class="den-tool-part-card-structured den-command-activity-details" data-testid="command-activity-details">
    <section class="den-tool-part-card-section" aria-label="Outcome">
      <dl class="den-tool-part-card-facts"><Index each={facts()}>{fact => <div><dt>{fact().label}</dt><dd data-tip={fact().value} data-tip-when-clipped="">{fact().value}</dd></div>}</Index></dl>

    </section>
    <Show when={activity().fact.steps?.length}>
      <section class="den-tool-part-card-section" aria-label="Activity steps">
        <h4 class="den-tool-part-card-section-label">Steps</h4>
        <KeyedIndex each={(activity().fact.steps ?? []).slice(stepPage() * 6, (stepPage() + 1) * 6)} keyOf={step => step.id}>{step => <div class="den-command-activity-step">
          <code>{step().command}</code><span>{step().count > 1 ? `${step().count.toLocaleString()} executions · ` : ""}{commandActivityStepOutcome(step())}</span>
        </div>}</KeyedIndex>
        <Show when={(activity().fact.steps?.length ?? 0) > 6}><div class="den-command-activity-paging" role="group" aria-label="Activity steps">
          <button type="button" class="den-chat-content-link" disabled={stepPage() === 0} onClick={() => setStepPage(value => value - 1)}>Previous steps</button>
          <button type="button" class="den-chat-content-link" disabled={(stepPage() + 1) * 6 >= (activity().fact.steps?.length ?? 0)} onClick={() => setStepPage(value => value + 1)}>Next steps</button>
        </div></Show>
      </section>
    </Show>
    <Show when={executions().length > 0}>
      <section class="den-tool-part-card-section" aria-label="Execution records">
        <h4 class="den-tool-part-card-section-label">Execution records</h4>
        <Show when={activity().fact.execution_ids.length}><div class="den-command-activity-paging" role="group" aria-label="Receipt scope">
          <button type="button" class="den-chat-content-link" aria-pressed={!wholeCommand()} onClick={() => {setWholeCommand(false);setReceiptCursors([]);}}>This activity</button>
          <button type="button" class="den-chat-content-link" aria-pressed={wholeCommand()} onClick={() => {setWholeCommand(true);setReceiptCursors([]);}}>Whole command</button>
        </div></Show>
        <KeyedIndex each={executions().slice(0, 6)} keyOf={execution => execution.id}>{execution => <div class="den-command-activity-step"><code>{execution().command.slice(0, 192)}{execution().command.length > 192 ? "…" : ""}</code><span>{execution().state}{execution().status_code !== undefined ? ` · exit ${execution().status_code}` : ""}</span></div>}</KeyedIndex>
        <Show when={executions().length > 6}><p class="den-tool-part-card-note">{executions().length} steps in this activity</p></Show>
        <Show when={receiptCursors().length > 0 || loaded()[0]?.executions_omitted}><div class="den-command-activity-paging" role="group" aria-label="Execution pages">
          <button type="button" class="den-chat-content-link" disabled={receiptCursors().length === 0} onClick={() => setReceiptCursors(value => value.slice(0,-1))}>Previous records</button>
          <button type="button" class="den-chat-content-link" disabled={!loaded()[0]?.executions_omitted} onClick={() => { const next = loaded()[0]?.next_cursor; if (next) setReceiptCursors(value => [...value,next]); }}>Next records</button>
        </div></Show>
        <ToolContentLink pane={`activity:${activity().fact.id}:receipts`} label="Execution receipts" content={{ kind: "inline", text: JSON.stringify({ activities: activity().members, facts: facts(), receipts: loaded(), feedback: props.part.feedback }, null, 2) }} projectId={props.projectId} />
      </section>
    </Show>
    <Show when={previews().length > 0}>
      <section class="den-tool-part-card-section" aria-label="Output">
        <h4 class="den-tool-part-card-section-label">Execution output preview</h4>
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
    <Show when={activity().decisions?.length}>
      <section class="den-tool-part-card-section" aria-label="Host decisions">
        <h4 class="den-tool-part-card-section-label">Host decisions</h4>
        <KeyedIndex each={activity().decisions ?? []} keyOf={decision => decision.key}>{decision => <p class="den-tool-part-card-note">{turnLoadRowSummary(decision().row).title} · {turnLoadRowSummary(decision().row).operation} · {turnLoadRowSummary(decision().row).state}</p>}</KeyedIndex>
      </section>
    </Show>
    <Show when={pages.state === "errored"}><p class="den-tool-part-card-note">Execution details are unavailable. The recorded activity is still shown.</p></Show>
  </div>;
}
