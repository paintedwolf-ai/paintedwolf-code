import { createMemo } from "solid-js";
import { createCommandActivityProjection, commandActivityHeadline, commandActivitySequence } from "../../chat/tool/command-activity-model.ts";
import { foldActionEntries } from "../../chat/action/part-action.ts";
import { commandProgramActive, preferredCommandProgram, useCommandActivitySnapshots } from "../../chat/tool/command-activity-snapshots.ts";
import { ActivitySpanCard, type ActivitySpanCardProps } from "./ActivitySpanCard.tsx";

export function CommandActivitySpan(props: ActivitySpanCardProps) {
  const project = createCommandActivityProjection();
  const snapshots = useCommandActivitySnapshots(() => props.entries, () => props.sessionId);
  const entries = createMemo(() => foldActionEntries(project(props.entries, snapshots)));
  const label = () => commandActivityHeadline(entries(), props.label);
  const running = () => props.entries.some(entry => {
    if (entry.kind !== "tool") return false;
    const program = preferredCommandProgram(entry.part.commandProgram, snapshots(entry.part));
    return program ? commandProgramActive(program) : entry.part.status === "running";
  });
  return <ActivitySpanCard {...props} label={label()} sequence={commandActivitySequence(entries())} entries={entries()} running={running()} />;
}
