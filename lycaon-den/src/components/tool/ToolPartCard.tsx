import type { WorkerTask } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { isTaskToolPart } from "../../chat/task/task-card-model.ts";
import type { TaskActivityContext } from "../../chat/task/task-card-model.ts";
import { isSkillToolPart } from "../../chat/skill/skill-card-model.ts";
import { isVerdictToolPart } from "../../chat/verdict/verdict-card-model.ts";
import { GenericToolCard } from "./GenericToolCard.tsx";
import { SkillCard } from "../skill/SkillCard.tsx";
import { TaskCard } from "../TaskCard.tsx";
import { VerdictCard } from "../verdict/VerdictCard.tsx";

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
};

/** Route tool parts to specialized or generic cards. */
export function ToolPartCard(props: Props) {
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
    return (
      <SkillCard
        part={props.part}
        layout={props.layout}
        sessionId={props.sessionId}
        projectId={props.projectId}
        rootRefs={props.rootRefs}
      />
    );
  }
  if (isVerdictToolPart(props.part)) {
    return (
      <VerdictCard
        part={props.part}
        layout={props.layout}
        sessionId={props.sessionId}
        projectId={props.projectId}
        rootRefs={props.rootRefs}
      />
    );
  }
  return (
    <GenericToolCard
      part={props.part}
      layout={props.layout}
      sessionId={props.sessionId}
      projectId={props.projectId}
      client={props.client}
      rootRefs={props.rootRefs}
    />
  );
}
