import { Show, createMemo } from "solid-js";
import type { WorkerTask } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { isTaskToolPart } from "../../chat/task/task-card-model.ts";
import type { TaskActivityContext } from "../../chat/task/task-card-model.ts";
import { isSkillToolPart } from "../../chat/skill/skill-card-model.ts";
import { isVerdictToolPart } from "../../chat/verdict/verdict-card-model.ts";
import { partAction } from "../../chat/action/part-action.ts";
import { GenericToolCard } from "./GenericToolCard.tsx";
import { commandActivityPart } from "../../chat/tool/command-activity-model.ts";
import { ActionCard } from "../action/ActionCard.tsx";
import { SkillCard } from "../skill/SkillCard.tsx";
import { TaskCard } from "../TaskCard.tsx";
import { VerdictCard } from "../verdict/VerdictCard.tsx";
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
  taskContext?: () => TaskActivityContext | undefined;
  onOpenWorker?: () => void;
  onOpenWorkerEvidence?: () => void;
  slots?: ToolBriefingSlots;
};

/** Routes worker calls to their worker card and every described action to the action card. */
export function ToolPartCard(props: Props) {
  const part = () => (props.part.commandProgram ? commandActivityPart(props.part) : props.part);
  // Worker cards open no activity detail, so they never ask for a summary.
  const slots = props.slots ?? (isTaskToolPart(props.part) ? undefined
    : useActivityBriefingSlots({ part, sessionId: () => props.sessionId, projectId: () => props.projectId }));
  const action = createMemo(() => (isTaskToolPart(props.part) ? undefined : partAction(part())));
  return (
    <Show when={action()} keyed fallback={<UndescribedToolPartCard {...props} slots={slots} />}>
      {(described) => (
        <ActionCard part={part()} action={described} layout={props.layout} sessionId={props.sessionId}
          projectId={props.projectId} client={props.client} rootRefs={props.rootRefs} slots={slots} />
      )}
    </Show>
  );
}

/** Calls the host has not described keep their family's card. */
function UndescribedToolPartCard(props: Props) {
  if (isTaskToolPart(props.part)) {
    return (
      <TaskCard
        part={props.part}
        layout={props.layout}
        sessionId={props.sessionId}
        projectId={props.projectId}
        rootRefs={props.rootRefs}
        worker={props.taskWorker}
        activityContext={props.taskContext}
        onOpenWorker={props.onOpenWorker}
        onOpenWorkerEvidence={props.onOpenWorkerEvidence}
      />
    );
  }
  if (isSkillToolPart(props.part)) {
    return <SkillCard part={props.part} slots={props.slots} layout={props.layout} sessionId={props.sessionId} projectId={props.projectId} rootRefs={props.rootRefs} />;
  }
  if (isVerdictToolPart(props.part)) {
    return <VerdictCard part={props.part} slots={props.slots} layout={props.layout} sessionId={props.sessionId} projectId={props.projectId} rootRefs={props.rootRefs} />;
  }
  return (
    <GenericToolCard part={props.part} slots={props.slots} layout={props.layout} sessionId={props.sessionId}
      projectId={props.projectId} client={props.client} rootRefs={props.rootRefs} />
  );
}
