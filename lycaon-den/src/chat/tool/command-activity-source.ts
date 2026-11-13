import type { CommandActivitySpan, HostSecretRedactionMeta } from "../../api/types.ts";
import { argRedactionField, spansForField } from "../transcript/content/redaction-spans.ts";
import type { CommandActivityView } from "./command-activity-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";

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

/** The command a call submitted, synthesized for form tools; undefined for other tools. */
export function activityCallCommand(call: ToolPartView): string | undefined {
  if (typeof call.args?.command === "string") return call.args.command;
  const build = formArgv[call.tool];
  return build ? build(call.args ?? {}).map(quoteWord).join(" ") : undefined;
}

/** What an activity ran; `redaction` locates host marks when the text is the whole submitted command. */
export type ActivityTargetText = {
  text: string;
  redaction?: { meta: HostSecretRedactionMeta; field: string };
};

const utf8 = new TextEncoder();
const utf8Decoder = new TextDecoder();

/**
 * The text at an activity's spans on one source unit. Offsets are UTF-8 bytes;
 * spans on other units index those units' bytes, never `source`.
 */
export function sliceProgramSource(source: string, spans: readonly CommandActivitySpan[], sourceRef: string): string | undefined {
  const bytes = utf8.encode(source);
  const ranges = spans
    .filter(span => span.source_ref === sourceRef && Number.isInteger(span.start_byte) && Number.isInteger(span.end_byte)
      && span.start_byte >= 0 && span.start_byte < span.end_byte && span.end_byte <= bytes.length)
    .map((span): [number, number] => [span.start_byte, span.end_byte])
    .sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const merged: [number, number][] = [];
  for (const range of ranges) {
    const last = merged.at(-1);
    if (last && range[0] <= last[1]) last[1] = Math.max(last[1], range[1]);
    else merged.push(range);
  }
  const slices: string[] = [];
  for (const [start, end] of merged) {
    const text = utf8Decoder.decode(bytes.subarray(start, end)).replace(/^\n+|\n+$/g, "");
    if (text && text !== slices.at(-1)) slices.push(text);
  }
  return slices.length ? slices.join("\n") : undefined;
}

/** The `command` argument as submitted, with the field its replacement marks are recorded against. */
export function submittedCommand(call: ToolPartView): ActivityTargetText | undefined {
  const text = call.args?.command;
  if (typeof text !== "string" || !text) return undefined;
  const redaction = call.argsRedaction
    ? { meta: call.argsRedaction.meta, field: argRedactionField(call.argsRedaction.fieldPrefix, "command") }
    : undefined;
  return { text, redaction };
}

function callTarget(call: ToolPartView, member: CommandActivityView["members"][number] | undefined): ActivityTargetText | undefined {
  const full = activityCallCommand(call);
  const subject = member?.subject ? { text: member.subject } : undefined;
  if (!full) return subject;
  const submitted = submittedCommand(call);
  const program = call.commandProgram;
  if (!program || (program.activities?.length ?? 0) <= 1 || !member) return submitted ?? { text: full };
  // A replacement changes the text's length, so source byte offsets past it no longer line up.
  const redaction = submitted?.redaction;
  if (redaction && spansForField({ host_secret_redaction: redaction.meta }, redaction.field).length) return subject;
  const slice = sliceProgramSource(full, member.spans, program.source_ref);
  return slice ? { text: slice } : subject;
}

/** One target per call in fold order; an activity of a compound command shows only its own statements. */
export function activityTargetCommands(activity: CommandActivityView): ActivityTargetText[] {
  const targets: ActivityTargetText[] = [];
  let memberIndex = 0;
  for (const call of activity.calls) {
    const member = call.commandProgram ? activity.members[memberIndex++] : undefined;
    const target = callTarget(call, member);
    if (target && target.text !== targets.at(-1)?.text) targets.push(target);
  }
  if (!targets.length && activity.fact.subject) targets.push({ text: activity.fact.subject });
  return targets;
}
