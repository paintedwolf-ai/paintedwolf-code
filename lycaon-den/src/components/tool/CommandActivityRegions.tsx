import { For, Index, Show, createMemo, type JSX } from "solid-js";
import type { CommandExecutionReceipt } from "../../api/types.ts";
import { activityStepsDiffer, commandActivityStepOutcome, type ActivityFact, type ActivityOutput } from "../../chat/tool/command-activity-details.ts";
import type { CommandActivityView } from "../../chat/tool/command-activity-model.ts";
import { activityCallCommand, activityTargetCommands } from "../../chat/tool/command-activity-source.ts";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { ChatContentDocument, CommandReceiptContent } from "../../chat/transcript/content/chat-content-document.ts";
import { turnLoadRowSummary } from "../../chat/turnload/turn-load-rows.ts";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { ActivityTarget } from "./ActivityTarget.tsx";
import { ToolContentLink } from "./ToolContentLink.tsx";

const SHOWN_ROWS = 3;

type ActivityProps = { activity: CommandActivityView; projectId?: string };

export function ActivityCommand(props: ActivityProps) {
  const targets = createMemo(() => activityTargetCommands(props.activity));
  return <ShowLatest when={targets()[0]}>
    {first => <ActivityTarget target={first()} extraCount={targets().length - 1} noun="command" />}
  </ShowLatest>;
}

/** Briefing text replaces the deterministic line; neither renders when empty. */
export function ActivityResult(props: { line?: string; children?: JSX.Element }) {
  return <Show when={props.children ?? props.line}>
    <div class="den-activity-result" data-testid="activity-result">{props.children ?? props.line}</div>
  </Show>;
}

export function ActivityOutputRegion(props: { output?: ActivityOutput }) {
  return <Show when={props.output}>{output => <div class="den-activity-output" data-testid="activity-output" data-stream={output().stream} data-tail={output().tail ? "" : undefined}>
    <pre class="den-activity-output__text"><Index each={output().lines}>{line => <>{line()}{"\n"}</>}</Index></pre>
    <Show when={output().hidden}>{hidden => <p class="den-activity-output__note">{hidden()}</p>}</Show>
  </div>}</Show>;
}

export function ActivitySteps(props: ActivityProps) {
  const steps = () => props.activity.fact.steps ?? [];
  return <Show when={activityStepsDiffer(steps())}>
    <div class="den-activity-rows" aria-label="Steps" role="group" data-testid="activity-steps">
      <For each={steps().slice(0, SHOWN_ROWS)}>{step => <div class="den-activity-row" data-state={step.state}>
        <code data-tip={step.command} data-tip-when-clipped="">{step.command}</code>
        <span>{step.count > 1 ? `${step.count.toLocaleString()} executions · ` : ""}{commandActivityStepOutcome(step)}</span>
      </div>}</For>
      <Show when={steps().length > SHOWN_ROWS}>
        <ToolContentLink short pane={`activity:${props.activity.fact.id}:steps`} label={`All ${steps().length.toLocaleString()} steps`} testId="activity-all-steps" projectId={props.projectId}
          content={{ kind: "inline", text: steps().map(step => `${step.command} · ${step.count.toLocaleString()} executions · ${commandActivityStepOutcome(step)}`).join("\n") }} />
      </Show>
    </div>
  </Show>;
}

function revealOffset(call: ToolPartView, field: "args" | "output") {
  const content = useToolContent();
  return () => content?.revealedCallId?.() === call.toolCallId ? (field === "args" ? content.argsOffset() : content.outputOffset()) : undefined;
}

function CallInputLink(props: ActivityProps & { call: ToolPartView; testId?: string }) {
  const content = useToolContent();
  const offset = revealOffset(props.call, "args");
  const revealingInput = () => content?.revealedCallId?.() === props.call.toolCallId && content.argsOffset() !== undefined;
  const command = createMemo(() => props.call.tool === "command" && !revealingInput() ? activityCallCommand(props.call) : undefined);
  const args = createMemo<ChatContentDocument["content"]>(() => props.call.argsReference
    ? { kind: "retained", reference: props.call.argsReference }
    : { kind: "inline", text: JSON.stringify(props.call.args ?? {}) });
  return <Show when={command() !== undefined} fallback={
    <ToolContentLink short label="Input" testId={props.testId} pane={`activity:${props.activity.fact.id}:input:${props.call.id}`}
      messageId={props.call.argsMessageId ?? props.call.assistantMessageId} toolCallId={props.call.toolCallId}
      content={args()} revealOffset={offset()} projectId={props.projectId} />}>
    <ToolContentLink short label="Input" testId={props.testId} pane={`activity:${props.activity.fact.id}:original:${props.call.id}`}
      messageId={props.call.argsMessageId ?? props.call.assistantMessageId} toolCallId={props.call.toolCallId}
      content={{ kind: "inline", text: command() ?? "", redaction: props.call.argsRedaction?.meta }} projectId={props.projectId} />
  </Show>;
}

function CallOutputLink(props: ActivityProps & { call: ToolPartView; testId?: string }) {
  const offset = revealOffset(props.call, "output");
  const output = createMemo<ChatContentDocument["content"]>(() => props.call.outputReference
    ? { kind: "retained", reference: props.call.outputReference }
    : { kind: "inline", text: props.call.output ?? "", redaction: props.call.redaction ?? undefined });
  return <Show when={props.call.outputReference || props.call.output}>
    <ToolContentLink short label="Output" testId={props.testId} pane={`activity:${props.activity.fact.id}:call-output:${props.call.id}`}
      messageId={props.call.messageId} toolCallId={props.call.toolCallId}
      content={output()} revealOffset={offset()} projectId={props.projectId} />
  </Show>;
}

/** Coalesced cards list their calls; a revealed call is always among the rows. */
export function ActivityCalls(props: ActivityProps) {
  const content = useToolContent();
  const calls = () => props.activity.calls;
  const shown = createMemo(() => {
    const first = calls().slice(0, SHOWN_ROWS);
    const revealed = calls().find(call => call.toolCallId === content?.revealedCallId?.());
    return revealed && !first.includes(revealed) ? [...first.slice(0, SHOWN_ROWS - 1), revealed] : first;
  });
  return <Show when={calls().length > 1}>
    <div class="den-activity-rows" aria-label="Calls" role="group" data-testid="activity-calls">
      <For each={shown()}>{call => <div class="den-activity-row" data-call={call.toolCallId}>
        <code data-tip={activityCallCommand(call) ?? call.tool} data-tip-when-clipped="">{activityCallCommand(call) ?? call.tool}</code>
        <span class="den-activity-row__links"><CallInputLink {...props} call={call} /><CallOutputLink {...props} call={call} /></span>
      </div>}</For>
      <Show when={calls().length > SHOWN_ROWS}>
        <ToolContentLink short pane={`activity:${props.activity.fact.id}:calls`} label={`All ${calls().length.toLocaleString()} calls`} testId="activity-all-calls" projectId={props.projectId}
          content={{ kind: "inline", text: calls().map(call => activityCallCommand(call) ?? call.tool).join("\n") }} />
      </Show>
    </div>
  </Show>;
}

export function ActivityExceptions(props: { facts: readonly ActivityFact[] }) {
  return <Show when={props.facts.length}>
    <dl class="den-activity-exceptions" data-testid="activity-exceptions">
      <Index each={props.facts}>{fact => <div><dt>{fact().label}</dt><dd data-tip={fact().value} data-tip-when-clipped="">{fact().value}</dd></div>}</Index>
    </dl>
  </Show>;
}

export function ActivityFooter(props: ActivityProps & {
  executions: readonly CommandExecutionReceipt[];
  receipts?: CommandReceiptContent;
  evidence?: JSX.Element;
}) {
  const single = () => props.activity.calls.length === 1 ? props.activity.calls[0] : undefined;
  const decisions = () => props.activity.decisions ?? [];
  return <nav class="den-activity-footer" aria-label="Activity records" data-testid="activity-footer">
    <Show when={single()} keyed>{call => <><CallInputLink {...props} call={call} testId="activity-input" /><CallOutputLink {...props} call={call} testId="activity-call-output" /></>}</Show>
    <Show when={props.activity.fact.job_handle} keyed>{handle => <ToolContentLink short pane={`activity:${props.activity.fact.id}:process`} label="Process output" testId="activity-process-output" content={{ kind: "process", handle }} projectId={props.projectId} />}</Show>
    <Show when={props.executions.length > 0 && props.receipts} keyed>
      {(receipts) => <ToolContentLink short pane={`activity:${props.activity.fact.id}:receipts`} label="Receipt" testId="command-activity-receipts" content={receipts} projectId={props.projectId} />}
    </Show>
    <Show when={decisions().length}>
      <ToolContentLink short pane={`activity:${props.activity.fact.id}:decisions`} label="Host decisions" testId="command-activity-decisions" projectId={props.projectId}
        content={{ kind: "inline", text: decisions().map(decision => { const row = turnLoadRowSummary(decision.row); return `${row.title} · ${row.operation} · ${row.state}`; }).join("\n") }} />
    </Show>
    {props.evidence}
  </nav>;
}
