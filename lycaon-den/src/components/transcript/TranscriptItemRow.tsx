import { useNoticesOptional } from "../../notices/notice-reporter.tsx";
import { Match, Show, Switch, createMemo } from "solid-js";
import type { Message, WorkerTask } from "../../api/types.ts";
import { type DisplayTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { type OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import type { WorkerTranscriptCache } from "../../chat/worker/worker-transcript.ts";
import { CheckpointDecisionChicklet } from "../checkpoint/CheckpointDecisionChicklet.tsx";
import { WorkflowFeedbackCard } from "../workflow/WorkflowFeedbackCard.tsx";
import { WorkflowExplainChicklet } from "../workflow/WorkflowExplainChicklet.tsx";
import { WorkflowBoundaryCard } from "../workflow/WorkflowBoundaryCard.tsx";
import { DraftRail } from "./DraftRail.tsx";
import { AssistantChatTurn } from "./AssistantChatTurn.tsx";
import { BlueprintCard } from "../blueprint/BlueprintCard.tsx";
import type { ChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import type { CitationExploreContext } from "../citation/CitationGroundingPanel.tsx";
import { ProgressStrip } from "../ProgressStrip.tsx";
import { TranscriptTimeMarker, TranscriptUnreadMarker, TurnTail } from "./TranscriptTimeRows.tsx";
import { TurnWalkCard } from "./TurnWalkCard.tsx";
import { type MessageRecoveryHandlers, UserBubble } from "./UserBubble.tsx";
import {
  WorkerTranscriptMessageRow,
  ActivityToolEntry,
  ActivitySpanRow,
  WorkerGroupRow,
  FileEditFolds,
} from "./WorkerTranscriptRows.tsx";

function messageBubbleItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "user" | "assistant" }> | null {
  return item.kind === "user" || item.kind === "assistant" ? item : null;
}

function activitySpanItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "activity_span" }> | null {
  return item.kind === "activity_span" ? item : null;
}

function toolItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "tool" }> | null {
  return item.kind === "tool" ? item : null;
}

function workerGroupItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "worker_group" }> | null {
  return item.kind === "worker_group" ? item : null;
}

function workerFileEditItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "worker_file_edit" }> | null {
  return item.kind === "worker_file_edit" ? item : null;
}

function fileEditItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "file_edit" }> | null {
  return item.kind === "file_edit" ? item : null;
}

function progressCompleteItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "progress_complete" }> | null {
  return item.kind === "progress_complete" ? item : null;
}

function progressUpdateItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "progress_update" }> | null {
  return item.kind === "progress_update" ? item : null;
}

function workflowFeedbackItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "workflow_feedback" }> | null {
  return item.kind === "workflow_feedback" ? item : null;
}

function workflowExplainItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "workflow_explain" }> | null {
  return item.kind === "workflow_explain" ? item : null;
}

function draftChatItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "draft" }> | null {
  return item.kind === "draft" ? item : null;
}

function assistantChatItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "assistant" }> | null {
  return item.kind === "assistant" ? item : null;
}

function userChatItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "user" | "pending_user" }> | null {
  return item.kind === "user" || item.kind === "pending_user" ? item : null;
}

function blueprintCardItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "blueprint_card" }> | null {
  return item.kind === "blueprint_card" ? item : null;
}

function checkpointItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "checkpoint" }> | null {
  return item.kind === "checkpoint" ? item : null;
}

function workflowBoundaryItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "workflow_boundary" }> | null {
  return item.kind === "workflow_boundary" ? item : null;
}

function fallbackItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "fallback" }> | null {
  return item.kind === "fallback" ? item : null;
}

function timeMarkerItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "time_marker" }> | null {
  return item.kind === "time_marker" ? item : null;
}

function unreadMarkerItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "unread_marker" }> | null {
  return item.kind === "unread_marker" ? item : null;
}

function turnTailItem(
  item: DisplayTranscriptItem,
): Extract<DisplayTranscriptItem, { kind: "turn_tail" }> | null {
  return item.kind === "turn_tail" ? item : null;
}

export function TranscriptItemRow(props: {
  item: () => DisplayTranscriptItem;
  taskMatches: () => ReadonlyMap<string, WorkerTask>;
  workers: WorkerTask[];
  messages: Message[];
  /** A row reads its own host message by id; the table is rebuilt once per update. */
  messageById: () => ReadonlyMap<string, Message>;
  /** Opening user message per terminal assistant row, resolved once per update. */
  reviewTurns: () => ReadonlyMap<string, string>;
  workerTranscripts?: WorkerTranscriptCache;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
  layout: TranscriptLayout;
  visibleTurnActive: boolean;
  sessionId?: string | null;
  checkpointClient?: import("../../api/client.ts").LycaonClient | null;
  checkpointAppStore?: import("../../store/app-state-model.ts").AppStore;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  branchJobId?: string;
  projectDir?: string;
  exploreContext?: CitationExploreContext;
  onExploreInSearch?: (query: string) => void;
  blueprint?: ChatBlueprintState;
  onBlueprintRevise?: (blueprintPath: string, text: string) => void;
  activePendingPhaseId?: string | null;
  recovery?: MessageRecoveryHandlers;
}) {
  const notices = useNoticesOptional();
  const item = () => props.item();
  const layout = () => props.layout;
  const bubbleText = () => messageBubbleItem(item())?.text ?? "";
  // Keyed host lookups stay memoized in the owner so a streaming update that
  // leaves this row's message unchanged does not re-render it.
  const chatKey = createMemo(() => {
    if (layout() !== "chat") return undefined;
    return (assistantChatItem(item()) ?? userChatItem(item()))?.key;
  });
  const wireRow = createMemo(() => {
    const key = chatKey();
    return key === undefined ? undefined : props.messageById().get(key);
  });
  const reviewTurn = createMemo(() => {
    const key = chatKey();
    return key === undefined ? null : props.reviewTurns().get(key) ?? null;
  });
  return (
    <Switch>
      <Match when={layout() === "chat" ? draftChatItem(item()) : null}>
        {(draft) => (
          <DraftRail
            sessionId={props.sessionId}
            slotId={draft().key}
            body={draft().text}
            versionCount={draft().draftVersionCount ?? 1}
            live={draft().live ?? false}
            generatingTokens={draft().generatingTokens ?? 0}
            client={props.checkpointClient}
          />
        )}
      </Match>
      <Match when={layout() === "chat" ? assistantChatItem(item()) : null}>
        {(assistant) => {
          return (
            <>
              <AssistantChatTurn
                item={assistant()}
                wireRow={wireRow}
                sessionId={props.sessionId}
                projectId={props.projectId}
                rootRefs={props.rootRefs}
                streaming={wireRow()?.status === "streaming"}
                client={props.checkpointClient}
                appStore={props.checkpointAppStore}
                exploreContext={props.exploreContext}
                onExploreInSearch={props.onExploreInSearch}
                onCopy={props.recovery?.onCopy}
              />
              <Show when={reviewTurn()} keyed>
                {(turn) => (
                  <TurnWalkCard
                    projectId={props.projectId ?? ""}
                    sessionId={props.sessionId ?? ""}
                    messageId={turn}
                    client={props.checkpointClient}
                  />
                )}
              </Show>
            </>
          );
        }}
      </Match>
      <Match when={layout() === "chat" ? userChatItem(item()) : null}>
        {(user) => {
          const wire = wireRow;
          const pending = () => {
            const row = user();
            return row.kind === "pending_user" ? row.pending : undefined;
          };
          const timestamp = () => {
            const entry = pending();
            return wire()?.created_at ?? (entry ? new Date(entry.createdAt).toISOString() : undefined);
          };
          const artifactIds = () => {
            const fromWire = (wire()?.artifact_ids ?? [])
              .map((id) => id.trim())
              .filter(Boolean);
            if (fromWire.length > 0) return fromWire;
            const row = user();
            return row.kind === "user" ? row.artifactIds ?? [] : [];
          };
          return (
            <UserBubble
              content={user().text}
              pending={pending()}
              contentParts={wire()?.content_parts}
              redaction={wire()?.host_secret_redaction}
              artifactIds={artifactIds()}
              sessionId={props.sessionId}
              projectId={props.projectId}
              rootRefs={props.rootRefs}
              entryKey={user().key}
              rowId={wire()?.id ?? user().key}
              client={props.checkpointClient}
              recovery={pending() ? undefined : props.recovery}
              ts={timestamp()}
            />
          );
        }}
      </Match>
      <Match when={item().kind === "user" && layout() === "worker"}>
        <WorkerTranscriptMessageRow
          role="user"
          content={bubbleText()}
          sessionId={props.sessionId}
          projectId={props.projectId}
          rootRefs={props.rootRefs}
        />
      </Match>
      <Match when={layout() === "worker" ? assistantChatItem(item()) : null}>
        {(assistant) => (
          <WorkerTranscriptMessageRow
            role="assistant"
            content={assistant().text}
            grounding={assistant().grounding}
            navigationRefs={assistant().navigationRefs}
            messageId={assistant().key}
            client={props.checkpointClient}
            sessionId={props.sessionId}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
          />
        )}
      </Match>
      <Match when={toolItem(item())}>
        {(tool) => {
          const card = () => (
            <ActivityToolEntry
              part={tool().part}
              matchedWorker={props.taskMatches().get(tool().part.id)}
              messages={props.messages}
              workerTranscripts={props.workerTranscripts}
              onOpenWorker={props.onOpenWorker}
              layout={props.layout}
              sessionId={props.sessionId}
              projectId={props.projectId}
              rootRefs={props.rootRefs}
              client={props.checkpointClient}
            />
          );
          return props.layout === "worker" ? (
            <div class="den-worker-msg--assistant den-worker-msg">
              <div class="den-worker-part den-worker-part--tool-card">{card()}</div>
            </div>
          ) : card();
        }}
      </Match>
      <Match when={workerGroupItem(item())}>
        {(group) => (
          <WorkerGroupRow
            item={group()}
            taskMatches={props.taskMatches()}
            messages={props.messages}
            workerTranscripts={props.workerTranscripts}
            onOpenWorker={props.onOpenWorker}
            layout={props.layout}
            sessionId={props.sessionId}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
            client={props.checkpointClient}
          />
        )}
      </Match>
      <Match when={activitySpanItem(item())}>
        {(span) => (
          <ActivitySpanRow
            item={span()}
            taskMatches={props.taskMatches()}
            messages={props.messages}
            workerTranscripts={props.workerTranscripts}
            onOpenWorker={props.onOpenWorker}
            layout={props.layout}
            sessionId={props.sessionId}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
            client={props.checkpointClient}
          />
        )}
      </Match>
      <Match when={workerFileEditItem(item())}>
        {(editItem) => (
          <FileEditFolds
            folds={editItem().folds}
            layout={props.layout}
            sessionId={props.sessionId}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
            jobId={null}
            entryKey={editItem().key}
          />
        )}
      </Match>
      <Match when={fileEditItem(item())}>
        {(editItem) => (
          <FileEditFolds
            folds={editItem().folds}
            layout={props.layout}
            sessionId={props.sessionId}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
            jobId={props.branchJobId}
            entryKey={editItem().key}
          />
        )}
      </Match>
      <Match when={progressCompleteItem(item())}>
        {(progress) => (
          <ProgressStrip
            steps={progress().steps}
            completed
            sessionId={props.sessionId ?? undefined}
            entryKey={progress().key}
          />
        )}
      </Match>
      <Match when={progressUpdateItem(item())}>
        {(update) => (
          <Switch>
            <Match when={update().initial}>
              <ProgressStrip
                steps={update().steps}
                summary={update().summary}
                created
                sessionId={props.sessionId ?? undefined}
                entryKey={update().key}
              />
            </Match>
            <Match when={update().summary != null || update().changes.length > 0}>
              <ProgressStrip
                deltaChanges={update().changes}
                summary={update().summary}
                sessionId={props.sessionId ?? undefined}
                entryKey={update().key}
              />
            </Match>
          </Switch>
        )}
      </Match>
      {/* Submit state persists until the host stamp arrives. */}
      <Match when={workflowFeedbackItem(item())}>
        {(feedback) => (
          <WorkflowFeedbackCard
            meta={feedback().meta}
            runId={feedback().runId}
            sessionId={props.sessionId}
            entryKey={feedback().key}
            client={props.checkpointClient}
            appStore={props.checkpointAppStore}
            activePendingPhaseId={props.activePendingPhaseId}
          />
        )}
      </Match>
      <Match when={workflowExplainItem(item())}>
        {(explain) => (
          <WorkflowExplainChicklet
            meta={explain().meta}
            runId={explain().runId}
            sessionId={props.sessionId}
            entryKey={explain().key}
            client={props.checkpointClient}
            appStore={props.checkpointAppStore}
          />
        )}
      </Match>
      <Match when={blueprintCardItem(item())} keyed>
        {(blueprintItem) => {
          const blueprintPath = blueprintItem.meta.blueprint_path;
          const blueprintState = props.blueprint;
          const wireContent = blueprintItem.content;
          const loadedContent = blueprintState?.blueprintContentFor(blueprintPath);
          return (
            <BlueprintCard
              anchorId={blueprintItem.key}
              sessionId={props.sessionId}
              entryKey={blueprintItem.key}
              view={blueprintItem.view}
              blueprintName={blueprintItem.meta.blueprint_title}
              content={loadedContent ?? wireContent}
              loading={blueprintState?.blueprintLoadingFor(blueprintPath) ?? false}
              warning={blueprintState?.blueprintWarningFor(blueprintPath) ?? null}
              projectId={props.projectId}
              onRevise={(text) => {
                blueprintState?.bindBlueprintPath(blueprintPath);
                props.onBlueprintRevise?.(blueprintPath, text);
              }}
              choiceTransitions={blueprintState?.choiceTransitions() ?? []}
              onOpen={() => {
                blueprintState?.bindBlueprintPath(blueprintPath);
                blueprintState?.openReview({ blueprintPath });
              }}
              onApprove={() => blueprintState?.approve(blueprintPath)}
              onReject={() => {
                blueprintState?.bindBlueprintPath(blueprintPath);
                blueprintState?.reject();
              }}
              onChoiceTransition={(id) => {
                blueprintState?.bindBlueprintPath(blueprintPath);
                blueprintState?.fireChoiceTransition(id);
              }}
            />
          );
        }}
      </Match>
      <Match when={checkpointItem(item())} keyed>
        {(checkpoint) => (
          <CheckpointDecisionChicklet
            projectId={props.projectId}
            meta={checkpoint.meta}
            sessionId={props.sessionId}
            entryKey={checkpoint.key}
            client={props.checkpointClient}
            onError={(err) => notices.reportError(err)}
          />
        )}
      </Match>
      <Match when={workflowBoundaryItem(item())}>
        {(boundary) => (
          <WorkflowBoundaryCard
            boundary={{
              key: boundary().key,
              messageId: boundary().key,
              label: boundary().label,
            }}
            sessionId={props.sessionId}
          />
        )}
      </Match>
      <Match when={layout() === "chat" ? timeMarkerItem(item()) : null}>
        {(marker) => <TranscriptTimeMarker item={marker()} />}
      </Match>
      <Match when={layout() === "chat" ? unreadMarkerItem(item()) : null}>
        {(marker) => <TranscriptUnreadMarker item={marker()} />}
      </Match>
      <Match when={layout() === "chat" ? turnTailItem(item()) : null}>
        {(tail) => <TurnTail item={tail()} />}
      </Match>
      <Match when={fallbackItem(item())}>
        {(fallback) => (
          <div
            class="den-transcript-fallback"
            data-msg-id={fallback().key}
            data-testid="transcript-fallback"
          >
            <strong>
              Unrecognized {fallback().messageKind ?? fallback().role} activity
            </strong>
            <Show when={fallback().text.trim()}>
              <pre>{fallback().text}</pre>
            </Show>
          </div>
        )}
      </Match>
    </Switch>
  );
}
