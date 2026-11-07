import { Index, Show, createEffect, createMemo, createResource, createSignal, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { CommandActivity } from "../../api/types.ts";
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
  /** Slot mounted above Outcome for downstream briefings. */
  summary?: JSX.Element;
};

const CALL_PAGE = 2;
const STEP_CHARS = 80;

export function CommandActivityCard(props: Props) {
  return <ToolPartShell {...props}><Show when={props.part.commandActivity}>{activity => <CommandActivityDetails {...props} activity={activity} />}</Show><TurnLoadFootLine part={props.part} /></ToolPartShell>;
}

function CommandActivityDetails(props: Props & { activity: () => NonNullable<ToolPartView["commandActivity"]> }) {
  const activity = props.activity;
  const content = useToolContent();
  const [callPage, setCallPage] = createSignal(0);
  createEffect(() => {
    const target = content?.revealedCallId?.();
    if (!target) return;
    const index = activity().calls.findIndex(call => call.toolCallId === target);
    if (index >= 0) setCallPage(Math.floor(index / CALL_PAGE));
  });
  const calls = createMemo(() => activity().calls.slice(callPage() * CALL_PAGE, (callPage() + 1) * CALL_PAGE));
  // Command calls and members fold in order; output-read calls carry no member.
  const memberByCall = createMemo(() => {
    const map = new Map<ToolPartView, CommandActivity>();
    let index = 0;
    for (const call of activity().calls) {
      if (call.tool !== "command") continue;
      const member = activity().members[index++];
      if (member) map.set(call, member);
    }
    return map;
  });
  const snapshot = createMemo(() => {
    const client = props.client ?? getLycaonClient();
    if (!client || !props.sessionId) return undefined;
    const seen = new Set<string>();
    const requests = calls().flatMap(call => {
      const program = call.commandProgram;
      if (!program || seen.has(program.id)) return [];
      seen.add(program.id);
      const member = memberByCall().get(call);
      return [{ program, activityId: member && member.execution_ids.length ? member.id : undefined }];
    });
    const cursor = undefined;
    const limit = 6;
    const key = JSON.stringify([requests.map(request => [request.program.id, request.program.state, request.program.settled_at, request.activityId]), activity().fact.state, activity().fact.execution_count, activity().fact.failed_count, cursor, limit]);
    return { client, sessionId: props.sessionId, requests, cursor, limit, key };
  }, undefined, { equals: (a,b) => a?.client === b?.client && a?.sessionId === b?.sessionId && a?.key === b?.key });
  const [pages] = createResource(snapshot, async ({ client, sessionId, requests, cursor, limit }) =>
    Promise.all(requests.map(({ program, activityId }) => client.getCommandProgramReceipt(sessionId, program.id, { limit, cursor, activity_id: activityId }))),
  );
  const loaded = () => pages.state === "ready" || pages.state === "refreshing" ? pages.latest ?? [] : [];
  const executions = createMemo(() => {
    const receipts = loaded().flatMap(page => page?.executions ?? []);
    return receipts;
  });
  const facts = createMemo(() => {
    const feedback = activity().fact.execution_ids.length === 0 ? props.part.feedback : props.part.feedback?.filter(entry => executions().some(row => row.stop_code === entry.code || row.host_stop_code === entry.code));
    const result = commandActivityFacts(activity(), executions(), feedback);
    const stop = loaded()[0]?.program.stop_code;
    if (activity().fact.execution_ids.length === 0 && stop && !result.some(fact => fact.value === stop)) result.push({ label: "Code", value: stop });
    return result;
  });
  const stepsLabel = createMemo(() => (activity().fact.steps?.length ?? 0) > 3 ? `All ${activity().fact.steps?.length ?? 0} steps` : "Step details");
  const summary = createMemo(() => commandActivityFacts(activity(), executions(), undefined).filter(fact => ["Outcome", "Launch outcome", "Effects", "Command stop", "Enclosing command"].includes(fact.label)));
  return <div class="den-tool-part-card-structured den-command-activity-details" data-testid="command-activity-details">
    <Show when={props.summary}>{props.summary}</Show>
    <section class="den-tool-part-card-section" aria-label="Outcome">
      <dl class="den-tool-part-card-facts"><Index each={summary()}>{fact => <div><dt>{fact().label}</dt><dd data-tip={fact().value} data-tip-when-clipped="">{fact().value}</dd></div>}</Index></dl>

    </section>
    <Show when={activity().fact.steps?.length}>
      <section class="den-tool-part-card-section" aria-label="Activity steps">
        <h4 class="den-tool-part-card-section-label">Steps</h4>
        <KeyedIndex each={(activity().fact.steps ?? []).slice(0, 3)} keyOf={step => step.id}>{step => <div class="den-command-activity-step">
          <code>{step().command.slice(0, STEP_CHARS)}{step().command.length > STEP_CHARS ? "…" : ""}</code><span>{step().count > 1 ? `${step().count.toLocaleString()} executions · ` : ""}{commandActivityStepOutcome(step())}</span>
        </div>}</KeyedIndex>
        <Show when={(activity().fact.steps?.length ?? 0) > 3 || activity().fact.steps?.some(step => step.command.length > STEP_CHARS)}><ToolContentLink pane={`activity:${activity().fact.id}:steps`} label={stepsLabel()} testId="command-activity-steps" content={{ kind: "inline", text: (activity().fact.steps ?? []).map(step => `${step.command} · ${step.count.toLocaleString()} executions · ${commandActivityStepOutcome(step)}`).join("\n") }} projectId={props.projectId} /></Show>
      </section>
    </Show>
    <Show when={executions().length > 0}>
      <section class="den-tool-part-card-section" aria-label="Execution records">
        <h4 class="den-tool-part-card-section-label">Execution records</h4>
        <ToolContentLink pane={`activity:${activity().fact.id}:receipts`} label="Execution receipts and diagnostics" testId="command-activity-receipts" content={{ kind: "command_receipts", programs: (snapshot()?.requests ?? []).map(({ program, activityId }) => ({ id: program.id, activityId })), context: JSON.stringify({ activities: activity().members, facts: facts(), feedback: props.part.feedback }) }} projectId={props.projectId} />
      </section>
    </Show>
    <CommandActivitySources activity={activity()} calls={calls()} offset={callPage() * CALL_PAGE} projectId={props.projectId}
      onPrevious={() => setCallPage(value => value - 1)} onNext={() => setCallPage(value => value + 1)} />
    <Show when={activity().decisions?.length}>
      <section class="den-tool-part-card-section" aria-label="Host decisions">
        <h4 class="den-tool-part-card-section-label">Host decisions</h4>
        <ToolContentLink pane={`activity:${activity().fact.id}:decisions`} label={`${activity().decisions?.length ?? 0} host decisions`} testId="command-activity-decisions" content={{ kind: "inline", text: (activity().decisions ?? []).map(decision => { const row = turnLoadRowSummary(decision.row); return `${row.title} · ${row.operation} · ${row.state}`; }).join("\n") }} projectId={props.projectId} />
      </section>
    </Show>
    <Show when={pages.state === "errored"}><p class="den-tool-part-card-note">Execution details are unavailable. The recorded activity is still shown.</p></Show>
  </div>;
}
