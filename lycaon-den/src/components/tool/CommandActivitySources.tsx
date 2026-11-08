import { Show, createSignal } from "solid-js";
import type { CommandActivityView } from "../../chat/tool/command-activity-model.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { ToolContentLink } from "./ToolContentLink.tsx";

// FormTool argv builders mirror SummarizeArgv, SurveyRepoArgv and CodeRewriteArgv
// in lycaon/internal/tools/native/form_tool.go, quoted like toolreach.QuoteProgram.
const plainWord = /^[A-Za-z0-9_.\/:=@%+,-]+$/;
const quoteWord = (word: string) => plainWord.test(word) ? word : `'${word.replaceAll("'", "'\\''")}'`;
const stringArg = (args: Record<string, unknown>, key: string) => typeof args[key] === "string" ? args[key] as string : "";
const appendValue = (words: string[], flag: string, value: string) => value === "" ? words : [...words, flag, value];
const appendPaths = (words: string[], args: Record<string, unknown>) => {
  words = appendValue(words, "--path", stringArg(args, "path"));
  const paths = Array.isArray(args.paths) ? args.paths : [];
  for (const path of paths) {
    if (typeof path === "string") words = appendValue(words, "--path", path);
  }
  return words;
};
const summarizeArgv = (args: Record<string, unknown>) => {
  let words = appendPaths(["summarize"], args);
  words = appendValue(words, "--content", stringArg(args, "content"));
  words = appendValue(words, "--focus", stringArg(args, "task"));
  words = appendValue(words, "--pattern", stringArg(args, "pattern"));
  return appendValue(words, "--cursor", stringArg(args, "cursor"));
};
const surveyRepoArgv = (args: Record<string, unknown>) =>
  appendValue(appendValue(["repo-map"], "--path", stringArg(args, "path")), "--bundle", stringArg(args, "bundle"));
const codeRewriteArgv = (args: Record<string, unknown>) => {
  let words = ["code-rewrite", "--pattern", stringArg(args, "pattern"), "--rewrite", stringArg(args, "rewrite")];
  words = appendPaths(words, args);
  if (args.recursive === true) words = [...words, "--recursive"];
  if (args.dry_run === true) words = [...words, "--dry-run"];
  words = appendValue(words, "--lang", stringArg(args, "lang"));
  return appendValue(words, "--syntax-override-reason", stringArg(args, "syntax_override_reason"));
};
const formArgv: Record<string, (args: Record<string, unknown>) => string[]> = {
  summarize: summarizeArgv,
  survey_repo: surveyRepoArgv,
  code_rewrite: codeRewriteArgv,
};

const command = (call: ToolPartView) => {
  if (typeof call.args?.command === "string") return call.args.command;
  const build = formArgv[call.tool];
  return build ? build(call.args ?? {}).map(quoteWord).join(" ") : undefined;
};

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
      <Show when={props.call.tool !== "command" || revealingInput()}><OriginalInputLink {...props} /></Show>
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
