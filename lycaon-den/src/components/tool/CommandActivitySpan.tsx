import { createMemo } from "solid-js";
import { createCommandActivityProjection, commandActivityHeadline } from "../../chat/tool/command-activity-model.ts";
import { liveCommandProgram } from "../../chat/tool/command-activity-live.ts";
import { ActivitySpanCard, type ActivitySpanCardProps } from "./ActivitySpanCard.tsx";

export function CommandActivitySpan(props: ActivitySpanCardProps) {
  const project = createCommandActivityProjection();
  const entries = createMemo(() => project(props.entries, part => liveCommandProgram(props.sessionId, part.toolCallId)));
  const label = () => commandActivityHeadline(entries(), props.label);
  return <ActivitySpanCard {...props} label={label()} entries={entries()} running={props.entries.some(entry => entry.kind === "tool" && entry.part.status === "running")} />;
}
