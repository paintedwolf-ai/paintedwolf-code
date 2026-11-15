import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { submittedCommand, type ActivityTargetText } from "./command-activity-source.ts";
import { toolPartSummaryTitle, type ToolPartView } from "./tool-part-model.ts";

export type ToolDetailTarget = {
  target: ActivityTargetText;
  noun: "command" | "target";
  /** Arguments the target already shows, left out of the card's input facts. */
  omitArguments?: ReadonlySet<string>;
};

const COMMAND_ARGUMENT: ReadonlySet<string> = new Set(["command"]);

/** What a tool card ran, shown first in its opened details. */
export function toolDetailTarget(part: ToolPartView): ToolDetailTarget | undefined {
  const command = part.kind === "command" ? submittedCommand(part) : undefined;
  if (command) return { target: command, noun: "command", omitArguments: COMMAND_ARGUMENT };
  const title = toolPartSummaryTitle(part).trim();
  if (!title || title === part.tool || title.toLowerCase() === formatSentenceCase(part.tool).toLowerCase()) return undefined;
  return { target: { text: title }, noun: "target" };
}
