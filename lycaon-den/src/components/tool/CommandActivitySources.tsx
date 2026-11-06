import { Show, createSignal } from "solid-js";
import type { CommandActivityView } from "../../chat/tool/command-activity-model.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { ToolContentLink } from "./ToolContentLink.tsx";

const command = (call: ToolPartView) => typeof call.args?.command === "string" ? call.args.command : undefined;

type CallProps = { call: ToolPartView; activity: CommandActivityView; projectId?: string };

function OriginalInputLink(props: CallProps) {
  const content = useToolContent();
  const offset = () => content?.revealedCallId?.() === props.call.toolCallId ? content.argsOffset() : undefined;
  return <ToolContentLink pane={`activity:${props.activity.fact.id}:input:${props.call.id}`} label="Original input"
    messageId={props.call.argsMessageId ?? props.call.assistantMessageId} toolCallId={props.call.toolCallId}
    content={props.call.argsReference ? { kind: "retained", reference: props.call.argsReference } : { kind: "inline", text: JSON.stringify(props.call.args ?? {}) }}
    revealOffset={offset()} projectId={props.projectId} />;
}

function CallOutputLink(props: CallProps) {
  const content = useToolContent();
  const offset = () => content?.revealedCallId?.() === props.call.toolCallId ? content.outputOffset() : undefined;
  return <Show when={props.call.outputReference || props.call.output}>
    <ToolContentLink pane={`activity:${props.activity.fact.id}:call-output:${props.call.id}`} label="Call output"
      messageId={props.call.messageId} toolCallId={props.call.toolCallId}
      content={props.call.outputReference ? { kind: "retained", reference: props.call.outputReference } : { kind: "inline", text: props.call.output ?? "", redaction: props.call.redaction ?? undefined }}
      revealOffset={offset()} projectId={props.projectId} />
  </Show>;
}

function CommandSource(props: CallProps & { text: string; copy: (text: string) => void }) {
  const content = useToolContent();
  const revealingInput = () => content?.revealedCallId?.() === props.call.toolCallId && content.argsOffset() !== undefined;
  return <div class="den-command-activity-source">

    <div class="den-command-activity-paging">
      <button data-testid="copy-command" type="button" class="den-chat-content-link" onClick={() => props.copy(props.text)}>Copy command</button>
      <ToolContentLink pane={`activity:${props.activity.fact.id}:original:${props.call.id}`} label="Full original command"
        messageId={props.call.argsMessageId ?? props.call.assistantMessageId} toolCallId={props.call.toolCallId}
        content={{ kind: "inline", text: props.text, redaction: props.call.argsRedaction?.meta }} projectId={props.projectId} />
      <Show when={revealingInput()}><OriginalInputLink {...props} /></Show>
      <CallOutputLink {...props} />
    </div>
  </div>;
}

export function CommandActivitySources(props: {
  activity: CommandActivityView; calls: readonly ToolPartView[]; offset: number; projectId?: string;
  onPrevious: () => void; onNext: () => void;
}) {
  const [copyState, setCopyState] = createSignal("");
  const copy = async (text: string) => {
    try { await writeClipboardText(text); setCopyState("Copied"); }
    catch { setCopyState("Could not copy command"); }
  };
  return <section class="den-tool-part-card-section" aria-label="Original command">
    <h4 class="den-tool-part-card-section-label">{props.activity.calls.some(call => command(call)) ? "Original command" : "Original input"}</h4>
    <Show when={props.activity.fact.job_handle} keyed>{handle => <ToolContentLink pane={`activity:${props.activity.fact.id}:process`} label="Process output" content={{ kind: "process", handle }} projectId={props.projectId} />}</Show>
    <KeyedIndex each={props.calls} keyOf={call => call.id}>{call => <Show when={command(call())} keyed fallback={<><OriginalInputLink call={call()} activity={props.activity} projectId={props.projectId} /><CallOutputLink call={call()} activity={props.activity} projectId={props.projectId} /></>}>
      {text => <CommandSource call={call()} activity={props.activity} projectId={props.projectId} text={text} copy={text => void copy(text)} />}
    </Show>}</KeyedIndex>
    <Show when={props.offset > 0 || props.calls.length < props.activity.calls.length}>
      <div class="den-command-activity-paging" role="group" aria-label="Activity calls">
        <button type="button" class="den-chat-content-link" disabled={props.offset === 0} onClick={props.onPrevious}>Previous calls</button>
        <span data-testid="command-activity-calls-range">{props.offset + 1}–{props.offset + props.calls.length} of {props.activity.calls.length}</span>
        <button type="button" class="den-chat-content-link" disabled={props.offset + props.calls.length >= props.activity.calls.length} onClick={props.onNext}>Next calls</button>
      </div>
    </Show>
    <p class="den-tool-part-card-note" role="status" hidden={!copyState()}>{copyState()}</p>
  </section>;
}
