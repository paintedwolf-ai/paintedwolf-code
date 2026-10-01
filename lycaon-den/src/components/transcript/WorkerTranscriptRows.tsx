import { Show, createMemo } from "solid-js";
import type { CitationGrounding, Message, NavigationReference, WorkerTask } from "../../api/types.ts";
import { buildProseCitationIndex, buildProseNavigationIndex } from "../../chat/markdown/prose-path-opens.ts";
import type { FileEditFold } from "../../chat/file-edit/file-edit-fold.ts";
import { TranscriptDiffGroup } from "./TranscriptDiffGroup.tsx";
import { type DisplayTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import { taskJobIdFromPart } from "../../chat/task/task-result-model.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { workerTranscriptMarkdownSource } from "../../chat/markdown/markdown-output.ts";
import { workerRowStatus, type OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import { lastWorkerToolActivity } from "../../chat/worker/worker-activity.ts";
import { workerTranscriptRows, type WorkerTranscriptCache } from "../../chat/worker/worker-transcript.ts";
import { MarkdownBody } from "./MarkdownBody.tsx";
import { AssistantProseBody } from "./AssistantProseBody.tsx";
import { InvocationRenderingSlot } from "./InvocationRenderingSlot.tsx";
import { ToolPartCard } from "../tool/ToolPartCard.tsx";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { ActivitySpanCard } from "../tool/ActivitySpanCard.tsx";
import { WorkerGroupCard } from "../worker/WorkerGroupCard.tsx";

export function WorkerTranscriptMessageRow(props: {
  role: "user" | "assistant";
  content: string;
  grounding?: CitationGrounding;
  navigationRefs?: NavigationReference[];
  messageId?: string;
  client?: import("../../api/client.ts").LycaonClient | null;
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
}) {
  const label = () => (props.role === "user" ? "You" : "Assistant");
  const citations = createMemo(() => buildProseCitationIndex(props.grounding));
  const navigation = createMemo(() =>
    buildProseNavigationIndex(props.navigationRefs),
  );
  return (
    <div
      class="den-worker-msg"
      classList={{
        "den-worker-msg--user": props.role === "user",
        "den-worker-msg--assistant": props.role === "assistant",
      }}
    >
      <article
        class="den-worker-msg-article"
        aria-label={label()}
        data-testid={
          props.role === "user"
            ? "transcript-article-user"
            : "transcript-article-assistant"
        }
      >
        <header class="den-worker-msg-header">
          <span class="den-worker-msg-role">
            {props.role === "user" ? "Prompt" : "Assistant"}
          </span>
        </header>
        <div class="den-worker-part den-worker-part--text">
          <Show
            when={props.role === "assistant"}
            fallback={
              <MarkdownBody
                source={workerTranscriptMarkdownSource(props.content)}
                literalHtml
                projectId={props.projectId}
                citations={citations()}
                navigation={navigation()}
                rootRefs={props.rootRefs}
              />
            }
          >
            <AssistantProseBody
              source={workerTranscriptMarkdownSource(props.content)}
              messageContent={props.content}
              literalHtml
              client={props.client}
              messageId={props.messageId}
              sessionId={props.sessionId}
              projectId={props.projectId}
              citations={citations()}
              navigation={navigation()}
              rootRefs={props.rootRefs}
            />
          </Show>
        </div>
      </article>
    </div>
  );
}

export function ActivityToolEntry(props: {
  part: ToolPartView;
  matchedWorker?: WorkerTask;
  messages: Message[];
  workerTranscripts?: WorkerTranscriptCache;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
  layout: TranscriptLayout;
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  client?: import("../../api/client.ts").LycaonClient | null;
}) {
  const matched = () => props.matchedWorker;
  const workerMessages = () => {
    const worker = matched();
    if (!worker) return undefined;
    return workerTranscriptRows(props.workerTranscripts, worker.id);
  };
  const taskContext = () => {
    const worker = matched();
    if (!worker) return undefined;
    return {
      worker,
      workerStatus: workerRowStatus(worker),
      lastActivity: lastWorkerToolActivity(
        workerMessages() ?? props.messages,
      ),
      workerMessages: workerMessages(),
    };
  };
  const openWorker = () => {
    const cb = props.onOpenWorker;
    if (!cb) return undefined;
    const workerId =
      matched()?.id ?? taskJobIdFromPart(props.part) ?? "";
    if (!workerId) return undefined;
    return () => cb(workerId);
  };
  const openWorkerEvidence = () => {
    const cb = props.onOpenWorker;
    if (!cb) return undefined;
    const workerId =
      matched()?.id ?? taskJobIdFromPart(props.part) ?? "";
    if (!workerId) return undefined;
    return () => cb(workerId, { scrollTo: "evidence" });
  };
  const toolCard = () => (
    <ToolPartCard
      part={props.part}
      layout={props.layout}
      sessionId={props.sessionId ?? undefined}
      projectId={props.projectId}
      client={props.client}
      rootRefs={props.rootRefs}
      taskWorker={matched}
      taskContext={taskContext}
      onOpenWorker={openWorker()}
      onOpenWorkerEvidence={openWorkerEvidence()}
    />
  );
  return (
    <>
      {toolCard()}
      <InvocationRenderingSlot
        client={props.client}
        sessionId={props.sessionId}
        assistantMessageId={props.part.assistantMessageId}
        toolCallId={props.part.toolCallId}
        mirrorHolderSessionId={props.part.childSessionId}
        layout={props.layout}
      />
    </>
  );
}

export function ActivitySpanRow(props: {
  item: Extract<DisplayTranscriptItem, { kind: "activity_span" }>;
  taskMatches: ReadonlyMap<string, WorkerTask>;
  messages: Message[];
  workerTranscripts?: WorkerTranscriptCache;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
  layout: TranscriptLayout;
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  client?: import("../../api/client.ts").LycaonClient | null;
}) {
  const card = (
    <ActivitySpanCard
      label={props.item.label}
      entries={props.item.entries}
      layout={props.layout}
      sessionId={props.sessionId ?? undefined}
      entryKey={props.item.key}
      renderToolEntry={(part) => (
        <ActivityToolEntry
          part={part()}
          matchedWorker={props.taskMatches.get(part().id)}
          messages={props.messages}
          workerTranscripts={props.workerTranscripts}
          onOpenWorker={props.onOpenWorker}
          layout={props.layout}
          sessionId={props.sessionId}
          projectId={props.projectId}
          rootRefs={props.rootRefs}
          client={props.client}
        />
      )}
    />
  );
  if (props.layout === "worker") {
    return (
      <div class="den-worker-msg--assistant den-worker-msg">
        <div class="den-worker-part den-worker-part--tool-card">{card}</div>
      </div>
    );
  }
  return card;
}

export function WorkerGroupRow(props: {
  item: Extract<DisplayTranscriptItem, { kind: "worker_group" }>;
  taskMatches: ReadonlyMap<string, WorkerTask>;
  messages: Message[];
  workerTranscripts?: WorkerTranscriptCache;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
  layout: TranscriptLayout;
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  client?: import("../../api/client.ts").LycaonClient | null;
}) {
  const card = (
    <WorkerGroupCard
      parts={props.item.parts}
      sessionId={props.sessionId ?? undefined}
      entryKey={props.item.key}
      renderWorkerEntry={(part) => (
        <ActivityToolEntry
          part={part()}
          matchedWorker={props.taskMatches.get(part().id)}
          messages={props.messages}
          workerTranscripts={props.workerTranscripts}
          onOpenWorker={props.onOpenWorker}
          layout={props.layout}
          sessionId={props.sessionId}
          projectId={props.projectId}
          rootRefs={props.rootRefs}
          client={props.client}
        />
      )}
    />
  );
  if (props.layout === "worker") {
    return (
      <div class="den-worker-msg--assistant den-worker-msg">
        <div class="den-worker-part den-worker-part--tool-card">{card}</div>
      </div>
    );
  }
  return card;
}

export function FileEditFolds(props: {
  folds: readonly FileEditFold[];
  layout: TranscriptLayout;
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  jobId?: string | null;
  entryKey: string;
}) {
  const group = (
    <TranscriptDiffGroup
      folds={props.folds}
      layout={props.layout}
      sessionId={props.sessionId}
      projectId={props.projectId}
      jobId={props.jobId}
      rootRefs={props.rootRefs}
      entryKey={props.entryKey}
    />
  );
  if (props.layout === "worker") {
    return <div class="den-worker-msg--file-edit den-worker-msg">{group}</div>;
  }
  return group;
}
