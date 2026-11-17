import { Show, createMemo } from "solid-js";
import type { BoardWorker, WorkerTask } from "../../api/types.ts";
import type { PendingWorkerApproval } from "../../chat/worker/worker-approval-model.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { isTaskToolPart } from "../../chat/task/task-card-model.ts";
import type { TaskActivityContext } from "../../chat/task/task-card-model.ts";
import { partAction, pendingAction } from "../../chat/action/part-action.ts";
import { commandActivityPart } from "../../chat/tool/command-activity-model.ts";
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
  const part = () => (props.part.commandProgram ? commandActivityPart(props.part) : props.part);
  const slots = props.slots ?? useActivityBriefingSlots({ part, sessionId: () => props.sessionId, projectId: () => props.projectId });
  const action = createMemo(() => partAction(part()) ?? pendingAction(part()));
  return (
    <ActionCard part={part()} action={action()} layout={props.layout} sessionId={props.sessionId}
      projectId={props.projectId} client={props.client} rootRefs={props.rootRefs} slots={slots} />
  );
}
