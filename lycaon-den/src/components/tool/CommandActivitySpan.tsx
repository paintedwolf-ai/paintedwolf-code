import { createEffect, createMemo, createSignal, createUniqueId, onCleanup } from "solid-js";
import type { ActivityEvent, Message } from "../../api/types.ts";
import { createCommandActivityProjection, commandActivityHeadline, commandActivitySequence } from "../../chat/tool/command-activity-model.ts";
import { commandActivityMemoryFor } from "../../chat/tool/command-activity-memory.ts";
import { subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import { toolPartDisplayStatus } from "../../chat/tool/tool-part-model.ts";
import { noteActivitySpanRowWithdrawals } from "../../chat/transcript/presentation/row-withdrawal-watch.ts";
import { foldActionEntries } from "../../chat/action/part-action.ts";
import { commandProgramActive, preferredCommandProgram, useCommandActivitySnapshots } from "../../chat/tool/command-activity-snapshots.ts";
import {
  isPendingWaitPart,
  resolveBlockingHostPhaseLabel,
  resolveWaitTargetLabel,
} from "../../chat/session/wait-target-activity.ts";
import { ActivitySpanCard, type ActivitySpanCardProps } from "./ActivitySpanCard.tsx";

export type CommandActivitySpanProps = ActivitySpanCardProps & {
  messages?: readonly Message[];
  activities?: readonly ActivityEvent[];
};

export function CommandActivitySpan(props: CommandActivitySpanProps) {
  const privateProject = createCommandActivityProjection();
  const privateScope = createUniqueId();
  const [processRevision, setProcessRevision] = createSignal(0);
  onCleanup(
    subscribeBackgroundProcessStore(() => {
      setProcessRevision((n) => n + 1);
    }, () => ({ sessionId: props.sessionId, statusOnly: true })),
  );
  const snapshots = useCommandActivitySnapshots(() => props.entries, () => props.sessionId);
  const entries = createMemo(() => {
    const project = props.sessionId ? createCommandActivityProjection(commandActivityMemoryFor(props.sessionId)) : privateProject;
    return foldActionEntries(project(props.entries, snapshots));
  });
  createEffect(() => {
    noteActivitySpanRowWithdrawals(props.sessionId ?? privateScope, props.entries, entries());
  });
  const label = () => commandActivityHeadline(entries(), props.label);
  const activeHeadline = () => {
    processRevision();
    const list = entries();
    for (let i = list.length - 1; i >= 0; i--) {
      const entry = list[i];
      if (entry?.kind !== "tool" || !isPendingWaitPart(entry.part, props.activities)) continue;
      const hostPhase = resolveBlockingHostPhaseLabel(props.activities, props.sessionId);
      if (hostPhase) return hostPhase;
      const target = resolveWaitTargetLabel(entry.part.args, props.sessionId, props.messages ?? []);
      if (target.state === "pending_delivery") return target.label;
      break;
    }
    return undefined;
  };
  const running = () => {
    processRevision();
    return props.entries.some((entry) => {
      if (entry.kind !== "tool") return false;
      if (isPendingWaitPart(entry.part, props.activities)) return true;
      const program = preferredCommandProgram(entry.part.commandProgram, snapshots(entry.part));
      return program ? commandProgramActive(program) : toolPartDisplayStatus(entry.part, props.sessionId) === "running";
    });
  };
  return (
    <ActivitySpanCard
      {...props}
      label={label()}
      activeHeadline={activeHeadline()}
      sequence={commandActivitySequence(entries())}
      entries={entries()}
      running={running()}
    />
  );
}
