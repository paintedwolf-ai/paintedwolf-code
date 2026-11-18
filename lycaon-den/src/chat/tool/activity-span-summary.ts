import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { indexWarmingChickletTitle } from "../transcript/projection/index-warming-copy.ts";
import { turnLoadRowSummary } from "../turnload/turn-load-rows.ts";
import { toolPartDisplayStatus, toolPartSummaryTitle, type ToolPartStatus } from "./tool-part-model.ts";

type ActivitySpanSummaryOptions = {
  label: string;
  verboseMode: boolean;
  running?: boolean;
  sessionId?: string;
};

export function activitySpanSummary(
  entries: readonly ActivitySpanEntry[],
  { label, verboseMode, running, sessionId }: ActivitySpanSummaryOptions,
) {
  const rows = entries.map((entry) => ({
    entry,
    status: entry.kind === "tool" ? toolPartDisplayStatus(entry.part, sessionId) : undefined,
  }));
  const toolStatuses = rows.flatMap((row) => row.status === undefined ? [] : [row.status]);
  const status = running || toolStatuses.includes("running")
    ? "running"
    : toolStatuses.at(-1) === "error" ? "error" : "done";
  const failureCount = verboseMode || status === "error"
    ? toolStatuses.filter((status) => status === "error").length
    : 0;
  const preferredStatus = !verboseMode && status === "done" ? "completed" : "error";
  return { status, failureCount, hint: activitySpanHint(rows, label, preferredStatus) };
}

function activitySpanHint(
  rows: readonly { entry: ActivitySpanEntry; status?: ToolPartStatus }[],
  label: string,
  preferredStatus: ToolPartStatus,
): string {
  const preferred = new Set<string>();
  const other = new Set<string>();
  for (const { entry, status } of rows) {
    const title = entryHintTitle(entry);
    if (title && title !== label) (status === preferredStatus ? preferred : other).add(title);
  }
  const [first, ...rest] = new Set([...preferred, ...other]);
  if (!first) return "";
  return rest.length ? `${first}, +${rest.length}` : first;
}

function entryHintTitle(entry: ActivitySpanEntry): string {
  if (entry.kind === "index_warming") return indexWarmingChickletTitle(entry.meta);
  if (entry.kind === "turn_load") return turnLoadRowSummary(entry.row).title;
  return toolPartSummaryTitle(entry.part);
}
