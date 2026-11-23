import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import { toolPartDisplayStatus, type ToolPartStatus } from "./tool-part-model.ts";

type ActivitySpanSummaryOptions = {
  verboseMode: boolean;
  running?: boolean;
  sessionId?: string;
};

export function activitySpanSummary(
  entries: readonly ActivitySpanEntry[],
  { verboseMode, running = false, sessionId }: ActivitySpanSummaryOptions,
) {
  let newest: ToolPartStatus = "completed";
  let active = running;
  let failures = 0;
  for (const entry of entries) {
    if (entry.kind !== "tool") continue;
    newest = toolPartDisplayStatus(entry.part, sessionId);
    active ||= newest === "running";
    if (newest === "error") failures++;
  }
  const status = active ? "running" : newest === "error" ? "error" : "done";
  return { status, failureCount: verboseMode || status === "error" ? failures : 0 };
}
