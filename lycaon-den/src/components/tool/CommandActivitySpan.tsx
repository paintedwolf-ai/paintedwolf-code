import { createMemo } from "solid-js";
import { createCommandActivityProjection, commandActivityHeadline } from "../../chat/tool/command-activity-model.ts";
import { useCommandActivitySnapshots } from "../../chat/tool/command-activity-snapshots.ts";
import { ActivitySpanCard, type ActivitySpanCardProps } from "./ActivitySpanCard.tsx";

export function CommandActivitySpan(props: ActivitySpanCardProps) {
  const project = createCommandActivityProjection();
  const snapshots = useCommandActivitySnapshots(() => props.entries, () => props.sessionId);
  const entries = createMemo(() => project(props.entries, snapshots));
  const label = () => commandActivityHeadline(entries(), props.label);
  return <ActivitySpanCard {...props} label={label()} entries={entries()} running={props.entries.some(entry => entry.kind === "tool" && (entry.part.commandProgram ? entry.part.commandProgram.state === "running" || entry.part.commandProgram.state === "waiting" : entry.part.status === "running"))} />;
}
