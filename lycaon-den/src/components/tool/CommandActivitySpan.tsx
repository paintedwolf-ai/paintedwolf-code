import { createEffect, createMemo, createUniqueId } from "solid-js";
import { createCommandActivityProjection, commandActivityHeadline, commandActivitySequence } from "../../chat/tool/command-activity-model.ts";
import { commandActivityMemoryFor } from "../../chat/tool/command-activity-memory.ts";
import { noteActivitySpanRowWithdrawals } from "../../chat/transcript/presentation/row-withdrawal-watch.ts";
import { foldActionEntries } from "../../chat/action/part-action.ts";
import { commandProgramActive, preferredCommandProgram, useCommandActivitySnapshots } from "../../chat/tool/command-activity-snapshots.ts";
import { ActivitySpanCard, type ActivitySpanCardProps } from "./ActivitySpanCard.tsx";

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
  const running = () => props.entries.some(entry => {
    if (entry.kind !== "tool") return false;
    const program = preferredCommandProgram(entry.part.commandProgram, snapshots(entry.part));
    return program ? commandProgramActive(program) : entry.part.status === "running";
  });
  return <ActivitySpanCard {...props} label={label()} sequence={commandActivitySequence(entries())} entries={entries()} running={running()} />;
}
