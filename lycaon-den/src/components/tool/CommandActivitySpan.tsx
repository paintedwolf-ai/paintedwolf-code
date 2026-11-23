import { createEffect, createMemo, createUniqueId } from "solid-js";
import { createCommandActivityProjection, commandActivityHeadline, commandActivitySequence } from "../../chat/tool/command-activity-model.ts";
import { commandActivityMemoryFor } from "../../chat/tool/command-activity-memory.ts";
import { noteActivitySpanRowWithdrawals } from "../../chat/transcript/presentation/row-withdrawal-watch.ts";
import { foldActionEntries } from "../../chat/action/part-action.ts";
import { commandProgramActive, preferredCommandProgram, useCommandActivitySnapshots } from "../../chat/tool/command-activity-snapshots.ts";
import { activitySpanToolParts } from "../../chat/tool/activity-span-model.ts";
import type { ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import { ActivitySpanCard, type ActivitySpanCardProps } from "./ActivitySpanCard.tsx";

export function findMatchingActivityToolEntry(
  entries: readonly ActivitySpanEntry[],
  toolCallId: string,
  preferredPath?: string,
): Extract<ActivitySpanEntry, { kind: "tool" }> | undefined {
  const matches = entries.filter(
    (entry): entry is Extract<ActivitySpanEntry, { kind: "tool" }> =>
      entry.kind === "tool" &&
      (entry.part.toolCallId === toolCallId ||
        (entry.part.commandActivity?.calls.some((call) => call.toolCallId === toolCallId) ?? false)),
  );
  if (matches.length <= 1 || !preferredPath) return matches[0];
  const normalized = preferredPath.trim();
  const base = normalized.split("/").pop() ?? normalized;
  return (
    matches.find((entry) => {
      const subject = entry.part.commandActivity?.fact.subject?.trim() ?? "";
      return (
        subject !== "" &&
        (subject === normalized ||
          normalized.endsWith(`/${subject}`) ||
          subject.endsWith(`/${normalized}`) ||
          (base.length > 0 && subject.includes(base)))
      );
    }) ?? matches[0]
  );
}

export function formatActivityCountChip(
  entries: readonly ActivitySpanEntry[],
  waitCount: number,
): string | null {
  const total = entries.length;
  if (waitCount > 0) {
    const nonWaits = entries.filter(
      (entry) => !(entry.kind === "tool" && entry.part.tool.toLowerCase() === "wait"),
    );
    const waitText = waitCount === 1 ? "1 wait" : `${waitCount} waits`;
    if (nonWaits.length === 0) return waitText;
    const allCommands = nonWaits.every(
      (entry) => entry.kind === "tool" && (entry.part.tool === "command" || Boolean(entry.part.commandActivity)),
    );
    const unit = allCommands
      ? nonWaits.length === 1 ? "command" : "commands"
      : nonWaits.length === 1 ? "action" : "actions";
    return `${nonWaits.length} ${unit} · ${waitText}`;
  }
  return total > 1 ? `×${total}` : null;
}

export function CommandActivitySpan(props: ActivitySpanCardProps) {
  const privateProject = createCommandActivityProjection();
  const privateScope = createUniqueId();
  const snapshots = useCommandActivitySnapshots(() => props.entries, () => props.sessionId);
  const entries = createMemo(() => {
    const project = props.sessionId ? createCommandActivityProjection(commandActivityMemoryFor(props.sessionId)) : privateProject;
    return foldActionEntries(project(props.entries, snapshots));
  });
  createEffect(() => {
    noteActivitySpanRowWithdrawals(props.sessionId ?? privateScope, props.entries, entries());
  });
  const label = () => commandActivityHeadline(entries(), props.label);
  const waitCount = () =>
    props.entries.filter((entry) => entry.kind === "tool" && entry.part.tool.toLowerCase() === "wait").length;
  const toolCallIds = () => {
    const ids = [...new Set(activitySpanToolParts(props.entries).map((p) => p.toolCallId).filter(Boolean))];
    return ids.length > 0 ? ids.join(" ") : undefined;
  };
  const running = () =>
    props.entries.some((entry) => {
      if (entry.kind !== "tool") return false;
      const program = preferredCommandProgram(entry.part.commandProgram, snapshots(entry.part));
      return program ? commandProgramActive(program) : entry.part.status === "running";
    });
  return (
    <ActivitySpanCard
      {...props}
      label={label()}
      sequence={commandActivitySequence(entries())}
      entries={entries()}
      waitCount={props.waitCount ?? waitCount()}
      toolCallIds={props.toolCallIds ?? toolCallIds()}
      running={running()}
    />
  );
}
