import { For, Show, createMemo, createResource, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { activityExecutions, commandSourceSegments } from "../../chat/tool/command-activity-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { ToolPartShell } from "./ToolPartShell.tsx";
import { ToolContentLink } from "./ToolContentLink.tsx";
import { RetainedToolContent } from "./RetainedToolContent.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  rootRefs?: readonly ResolveProjectRoot[];
};

const CALL_PAGE = 3;
const PREVIEW_BYTES = 480;

export function CommandActivityCard(props: Props) {
  return <ToolPartShell {...props}><CommandActivityDetails {...props} /></ToolPartShell>;
}

function CommandActivityDetails(props: Props) {
  const activity = () => props.part.commandActivity!;
  const [callPage, setCallPage] = createSignal(0);
  const [receiptCursors, setReceiptCursors] = createSignal<string[]>([]);
  const receiptCursor = () => receiptCursors().at(-1) ?? activity().continuation?.cursor;
  const calls = createMemo(() => activity().calls.slice(callPage() * CALL_PAGE, (callPage() + 1) * CALL_PAGE));
  const [pages] = createResource(
    () => {
      const client = props.client ?? getLycaonClient();
      if (!client || !props.sessionId) return undefined;
      return { client, sessionId: props.sessionId, calls: calls(), state: activity().fact.state, cursor: receiptCursor() };
    },
    async ({ client, sessionId, calls, cursor }) => Promise.all(calls.map(async call => {
      const program = call.commandProgram;
      return program ? client.getCommandProgramReceipt(sessionId, program.id, { limit: activity().continuation ? 6 : 32, cursor }) : undefined;
    })),
  );
  const loaded = () => pages.state === "ready" || pages.state === "refreshing" ? pages.latest ?? [] : [];
  const executions = createMemo(() => {
    const receipts = loaded().flatMap(page => page?.executions ?? []);
    return activity().continuation ? receipts : activityExecutions(activity(), receipts);
  });
  const facts = createMemo(() => {
    const value = activity().fact;
    const result = [{ label: "Outcome", value: value.outcome }];
    if (value.directory) result.push({ label: "Directory", value: value.directory });
    if (activity().members.length > 1) result.push({ label: "Observations", value: `${activity().members.length} reads; each keeps its own receipt` });
    for (const execution of executions()) {
      if (execution.stop_code) result.push({ label: "Code", value: execution.stop_code });
      if (execution.host_stop_code && execution.host_stop_code !== execution.stop_code) result.push({ label: "Host stop", value: execution.host_stop_code });
      if (execution.revision_conflict) result.push({ label: "Conflict", value: execution.revision_conflict.path });
      if (execution.accepted_revision) result.push({ label: "Accepted revision", value: execution.accepted_revision });
      if (execution.published_revision) result.push({ label: "Published revision", value: execution.published_revision });
      if (execution.approval_ref) result.push({ label: "Approval", value: execution.approval_ref });
      if (execution.recovery_ref) result.push({ label: "Recovery", value: execution.recovery_ref });
    }
    return result;
  });
  const previews = createMemo(() => executions().flatMap(execution => (execution.outputs ?? []).map(output => ({ ...output, execution }))));
  const source = (call: ToolPartView): string | undefined => typeof call.args?.command === "string" ? call.args.command : undefined;
  const [copyState, setCopyState] = createSignal("");
  const copy = async (text: string) => {
    try { await writeClipboardText(text); setCopyState("Copied"); }
    catch { setCopyState("Could not copy command"); }
  };
  return <div class="den-tool-part-card-structured den-command-activity-details" data-testid="command-activity-details">
    <section class="den-tool-part-card-section" aria-label="Outcome">
      <dl class="den-tool-part-card-facts"><For each={facts()}>{fact => <div><dt>{fact.label}</dt><dd>{fact.value}</dd></div>}</For></dl>
    </section>
    <Show when={executions().length > 0}>
      <section class="den-tool-part-card-section" aria-label="Execution">
        <h4 class="den-tool-part-card-section-label">Execution</h4>
        <For each={executions().slice(0, 6)}>{execution => <div class="den-command-activity-step"><code>{execution.command.slice(0, 192)}{execution.command.length > 192 ? "…" : ""}</code><span>{execution.state}{execution.status_code !== undefined ? ` · exit ${execution.status_code}` : ""}</span></div>}</For>
        <Show when={executions().length > 6}><p class="den-tool-part-card-note">{executions().length} steps in this activity</p></Show>
        <Show when={activity().continuation}><div class="den-command-program-paging" role="group" aria-label="Execution pages">
          <button type="button" class="den-chat-content-link" disabled={receiptCursors().length === 0} onClick={() => setReceiptCursors(value => value.slice(0,-1))}>Previous operations</button>
          <button type="button" class="den-chat-content-link" disabled={!loaded()[0]?.executions_omitted} onClick={() => { const next = loaded()[0]?.next_cursor; if (next) setReceiptCursors(value => [...value,next]); }}>Next operations</button>
        </div></Show>
        <ToolContentLink pane={`activity:${activity().fact.id}:receipts`} label="Execution receipts" content={{ kind: "inline", text: JSON.stringify(executions(), null, 2) }} projectId={props.projectId} />
      </section>
    </Show>
    <Show when={previews().length > 0}>
      <section class="den-tool-part-card-section" aria-label="Output">
        <h4 class="den-tool-part-card-section-label">Output preview</h4>
        <For each={previews().slice(-1)}>{output => <div>
          <pre class="den-command-program-output-preview">{output.preview.slice(0, PREVIEW_BYTES)}</pre>
          <Show when={!output.complete}><p class="den-tool-part-card-note">Incomplete {output.stream} stream</p></Show>
          <ToolContentLink pane={`activity:${activity().fact.id}:${output.execution.id}:${output.stream}`} label={`${output.stream} preview`} content={{ kind: "inline", text: output.preview }} projectId={props.projectId} />
          <Show when={output.preview_truncated || output.preview.length > PREVIEW_BYTES}><p class="den-tool-part-card-note">Preview is limited; retained reference: {output.ref}</p></Show>
        </div>}</For>
      </section>
    </Show>
    <section class="den-tool-part-card-section" aria-label="Original command">
      <h4 class="den-tool-part-card-section-label">Original command</h4>
      <For each={calls()}>{call => <Show when={source(call)} keyed fallback={<RetainedToolContent field="args" />}>{text => {
        const program = () => call.commandProgram ?? props.part.commandProgram!;
        const spans = () => ({ ...activity().fact, spans: activity().members.flatMap(member => member.spans) });
        const segments = () => commandSourceSegments(text.slice(0, PREVIEW_BYTES), program(), spans(), !!call.argsRedaction);
        return <div class="den-command-activity-source">
          <pre class="den-command-activity-command"><For each={segments()}>{segment => segment.highlighted ? <mark>{segment.text}</mark> : segment.text}</For>{text.length > PREVIEW_BYTES ? "…" : ""}</pre>
          <div class="den-command-program-paging">
            <button type="button" class="den-chat-content-link" onClick={() => void copy(text)}>Copy command</button>
            <ToolContentLink pane={`activity:${activity().fact.id}:original:${call.id}`} label="Full original command" messageId={call.argsMessageId ?? call.assistantMessageId} content={{ kind: "inline", text, redaction: call.argsRedaction?.meta }} projectId={props.projectId} />
          </div>
        </div>;
      }}</Show>}</For>
      <Show when={activity().calls.length > CALL_PAGE}><div class="den-command-program-paging" role="group" aria-label="Activity calls">
        <button type="button" class="den-chat-content-link" disabled={callPage() === 0} onClick={() => setCallPage(value => value - 1)}>Previous calls</button>
        <span>{callPage() * CALL_PAGE + 1}–{Math.min((callPage() + 1) * CALL_PAGE, activity().calls.length)} of {activity().calls.length}</span>
        <button type="button" class="den-chat-content-link" disabled={(callPage() + 1) * CALL_PAGE >= activity().calls.length} onClick={() => setCallPage(value => value + 1)}>Next calls</button>
      </div></Show>
      <Show when={copyState()}><p class="den-tool-part-card-note" role="status">{copyState()}</p></Show>
    </section>
    <Show when={pages.state === "errored"}><p class="den-tool-part-card-note">Execution details are unavailable. The recorded activity is still shown.</p></Show>
  </div>;
}
