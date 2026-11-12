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
