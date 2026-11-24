import { Show, createMemo, createSignal, onCleanup } from "solid-js";
import type { ActivityEvent, BoardWorker, Message, WorkerTask } from "../../api/types.ts";
import type { PendingWorkerApproval } from "../../chat/worker/worker-approval-model.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { isTaskToolPart } from "../../chat/task/task-card-model.ts";
import type { TaskActivityContext } from "../../chat/task/task-card-model.ts";
import { partAction, pendingAction } from "../../chat/action/part-action.ts";
import { commandActivityPart } from "../../chat/tool/command-activity-facts.ts";
import { subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import {
  isPendingWaitPart,
  resolveBlockingHostPhaseLabel,
  resolveWaitTargetLabel,
} from "../../chat/session/wait-target-activity.ts";
import { ActionCard } from "../action/ActionCard.tsx";
import { TaskCard } from "../TaskCard.tsx";
import type { ToolBriefingSlots } from "./tool-briefing-slots.ts";
import { useActivityBriefingSlots } from "../../chat/tool/activity-briefing.tsx";

type Props = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  client?: LycaonClient | null;
  rootRefs?: readonly ResolveProjectRoot[];
  messages?: readonly Message[];
  activities?: readonly ActivityEvent[];
  taskWorker?: () => WorkerTask | undefined;
  taskBoardWorker?: () => BoardWorker | undefined;
  taskPendingApproval?: () => PendingWorkerApproval | undefined;
  taskContext?: () => TaskActivityContext | undefined;
  onOpenWorker?: () => void;
  onOpenWorkerEvidence?: () => void;
  slots?: ToolBriefingSlots;
};

/** Worker calls keep their worker card; every other call is an action card. */
export function ToolPartCard(props: Props) {
  return (
    <Show when={!isTaskToolPart(props.part)} fallback={
      <TaskCard
        part={props.part}
        layout={props.layout}
        sessionId={props.sessionId}
        projectId={props.projectId}
        rootRefs={props.rootRefs}
        worker={props.taskWorker}
        boardWorker={props.taskBoardWorker}
        pendingApproval={props.taskPendingApproval}
        activityContext={props.taskContext}
        onOpenWorker={props.onOpenWorker}
        onOpenWorkerEvidence={props.onOpenWorkerEvidence}
      />
    }>
      <DescribedToolPartCard {...props} />
    </Show>
  );
}

function DescribedToolPartCard(props: Props) {
  const [processRevision, setProcessRevision] = createSignal(0);
  onCleanup(
    subscribeBackgroundProcessStore(() => {
      setProcessRevision((n) => n + 1);
    }, () => ({ sessionId: props.sessionId, statusOnly: true })),
  );
  const part = () => (props.part.commandProgram ? commandActivityPart(props.part) : props.part);
  const slots = props.slots ?? useActivityBriefingSlots({ part, sessionId: () => props.sessionId, projectId: () => props.projectId });
  const waitOutcome = createMemo(() => {
    processRevision();
    const current = part();
    if (!isPendingWaitPart(current, props.activities)) return undefined;
    const hostPhase = resolveBlockingHostPhaseLabel(props.activities, props.sessionId);
    const target = resolveWaitTargetLabel(current.args, props.sessionId, props.messages ?? []);
    return hostPhase ?? (target.state !== "condition_default" ? target.label : undefined);
  });
  const action = createMemo(() => {
    const current = part();
    const base = partAction(current) ?? pendingAction(current);
    const liveTitle = waitOutcome();
    if (!liveTitle) {
      return isPendingWaitPart(current, props.activities) ? { ...base, status: "running" as const } : base;
    }
    return {
      ...base,
      status: "running" as const,
      ran: { ...base.ran, text: liveTitle },
    };
  });
  return (
    <ActionCard part={part()} action={action()} liveOutcome={waitOutcome()} layout={props.layout} sessionId={props.sessionId}
      projectId={props.projectId} client={props.client} rootRefs={props.rootRefs} slots={slots} />
  );
}
