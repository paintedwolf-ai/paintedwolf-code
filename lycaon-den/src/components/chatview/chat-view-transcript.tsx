import { Show, createSignal, onCleanup } from "solid-js";
import { attachThemedViewportScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { DEN_SCROLLPORT_AXIS_ATTR, DEN_SCROLLPORT_CLASS, DEN_SCROLLPORT_CONTENT_CLASS, DEN_SCROLLPORT_VIEWPORT_CLASS } from "../../platform/scrolling/scrollport-frame-dom.ts";
import { WorkflowStartProposalCard } from "../workflow/WorkflowStartProposalCard.tsx";
import { getLycaonClient, type noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { ChatSpanBlocks } from "../transcript/ChatSpanBlocks.tsx";
import type { TranscriptTailSlot } from "../transcript/SessionTranscript.tsx";
import { pendingWorkflowFeedback } from "../../workflow/workflow-feedback-model.ts";
import { NetworkActionContext } from "../../chat/network-action-context.ts";
import { ChatDestinationScope, chatDestinationOf } from "../../chat/composer/chat-destination-scope.tsx";
import { StreamScrollJump } from "../../chat/stream/stream-scroll-jump.tsx";
import { TranscriptDayChip } from "../../chat/stream/transcript-day-chip.tsx";
import type { createChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import type { createChatWorkflowState } from "./chat-workflow-state.ts";
import { openInSearch } from "../../search/search-nav.ts";
import { hasTurnOutcome, sessionIdleDisposition } from "../../chat/recovery/turn-outcome.ts";
import { TurnOutcomeMarker } from "../transcript/TurnOutcomeMarker.tsx";
import type { Accessor } from "solid-js";
import type { ChatViewProps } from "./chat-view-props.ts";
import type { createChatViewActivity } from "./chat-view-activity.ts";
import type { createChatViewRecovery } from "./chat-view-recovery.ts";
import type { ComposerSendPayload } from "./Composer.tsx";
import type { createChatViewTranscript } from "./chat-view-transcript-runtime.ts";
import type { createTranscriptViewportController } from "../../chat/stream/transcript-viewport.tsx";
type Viewport = ReturnType<typeof createTranscriptViewportController>;
type Activity = ReturnType<typeof createChatViewActivity>;

type TranscriptOptions = {
  chat: ChatViewProps;
  activity: Activity;
  viewport: Viewport;
  runtime: ReturnType<typeof createChatViewTranscript>;
  workflow: ReturnType<typeof createChatWorkflowState>;
  blueprint: ReturnType<typeof createChatBlueprintState>;
  recoveryState: ReturnType<typeof createChatViewRecovery>;
  surfaceActive: Accessor<boolean>;
  sendWithStreamFollow: (payload: ComposerSendPayload) => Promise<boolean | void>;
  connectedClient: () => ReturnType<typeof getLycaonClient>;
  chatNotices: () => ReturnType<typeof noticeReporterFor>;
};

export function ChatViewTranscript(options: TranscriptOptions) {
  const props = options.chat;
  const viewport = options.viewport;
  const workflow = options.workflow;
  const blueprint = options.blueprint;
  const surfaceActive = options.surfaceActive;
  const connectedClient = options.connectedClient;
  const chatNotices = options.chatNotices;
  const sendWithStreamFollow = options.sendWithStreamFollow;
  const { streamEl, setStreamEl } = options.runtime;
  const { workers, spanBlocks, presentedMessages, chatLive, activeRun } = options.activity;
  const { recovery, turnHasProgress, retryLastAsk, keepGoingLastAsk, rewindAndRetryLastAsk } = options.recoveryState;
  // Block writes a global deny and mirrors it optimistically.
  const [blockedHosts, setBlockedHosts] = createSignal<ReadonlySet<string>>(
    new Set(),
  );
  const networkActions = {
    blockHost: async (host: string) => {
      const target = host.trim();
      const client = getLycaonClient();
      if (!target || !client) return;
      try {
        const current = await client.getApprovalsSettings();
        const rules = [
          ...current.rules.filter(
            (r) => !(r.category === "host" && r.pattern === target),
          ),
          {
            category: "host" as const,
            pattern: target,
            effect: "deny" as const,
          },
        ];
        await client.updateApprovalsSettings({ rules });
        setBlockedHosts((prev) => new Set(prev).add(target));
      } catch (err) {
        chatNotices().reportError(err);
      }
    },
    isBlocked: (host: string) => blockedHosts().has(host.trim()),
  };

  // Tail slots preserve transcript reading order.
  const transcriptTail: readonly TranscriptTailSlot[] = [
    {
      id: "turn-outcome",
      present: () =>
        !chatLive() &&
        (props.appStore.state.pendingSends[props.sessionId]?.length ?? 0) === 0 &&
        hasTurnOutcome(sessionIdleDisposition(props.sessionId)),
      children: () => (
        <TurnOutcomeMarker
          disposition={sessionIdleDisposition(props.sessionId)}
          hasProgress={turnHasProgress()}
          onRetry={() => void retryLastAsk()?.()}
          onKeepGoing={() => void keepGoingLastAsk()?.()}
          onRewindAndRetry={() => void rewindAndRetryLastAsk()?.()}
        />
      ),
    },
    {
      id: "workflow-start-proposal",
      present: () => workflow.startProposal() != null,
      children: () => (
        <Show when={workflow.startProposal()} keyed>
          {(proposal) => (
            <WorkflowStartProposalCard
              proposal={proposal}
              catalog={props.appStore.state.workflowCatalog}
              busy={workflow.busy()}
              onStart={() => workflow.startProposalWorkflow(proposal)}
              onOpenDrawer={() => workflow.openDrawerWithPicker()}
              onDismiss={() => workflow.dismissProposal()}
            />
          )}
        </Show>
      ),
    },
  ];

  return (
          <div class="den-chat-stream-wrap">
            {/* The outer frame keeps scrollbar chrome stationary. */}
            <div
              class={`${DEN_SCROLLPORT_CLASS} den-chat-stream-frame`}
              {...{ [DEN_SCROLLPORT_AXIS_ATTR]: "y" }}
              data-session-id={props.sessionId}
            >
            <div
              class={`${DEN_SCROLLPORT_VIEWPORT_CLASS} den-chat-stream`}
              ref={(el) => {
                let detachScrollbar: (() => void) | undefined;
                queueMicrotask(() => {
                  const frame = el.parentElement;
                  if (!el.isConnected || !(frame instanceof HTMLDivElement)) return;
                  const body = el.querySelector<HTMLElement>(".den-chat-stream-body");
                  detachScrollbar = attachThemedViewportScrollbar(frame, el, {
                    axis: "y",
                    ...(body ? { extent: body } : {}),
                  });
                  // Stream publication follows motion-controller attachment.
                  viewport.attachStream(el);
                  setStreamEl(el);
                });
                onCleanup(() => {
                  if (streamEl() === el) setStreamEl(undefined);
                  if (viewport.stream() === el) viewport.attachStream(null);
                  detachScrollbar?.();
                });
              }}
              data-testid="chat-stream"
              data-session-id={props.sessionId}
            >
              <div class={`${DEN_SCROLLPORT_CONTENT_CLASS} den-chat-stream-body`}>
                <NetworkActionContext.Provider value={networkActions}>
                  <ChatDestinationScope destination={chatDestinationOf(props.projectId, props.sessionId)}>
                    <ChatSpanBlocks
                      blocks={spanBlocks()}
                      messages={presentedMessages()}
                      sessionId={props.sessionId}
                      surfaceActive={surfaceActive}
                      visibleTurnActive={chatLive()}
                      workers={workers()}
                      workerTranscripts={
                        props.appStore.state.workerTranscripts
                      }
                      onOpenWorker={(id, opts) =>
                        props.onOpenWorker(id, opts)
                      }
                      checkpointClient={connectedClient()}
                      checkpointAppStore={props.appStore}
                      projectDir={props.projectDir}
                      projectId={props.projectId}
                      rootRefs={
                        (
                          props.projects.byId(props.projectId) ??
                          props.projects.state.projects.find(
                            (p) => p.id === props.projectId,
                          )
                        )?.roots ?? []
                      }
                      onExploreInSearch={(query) => {
                        void openInSearch(props.projectId, query);
                      }}
                      blueprint={blueprint}
                      onBlueprintRevise={(blueprintPath, text) => {
                        blueprint.bindBlueprintPath(blueprintPath);
                        void sendWithStreamFollow({ text });
                      }}
                      activePendingPhaseId={
                        pendingWorkflowFeedback(activeRun())?.phase_id ?? null
                      }
                      recovery={recovery}
                      pendingSends={viewport.presentsLiveTail() ? props.appStore.state.pendingSends[props.sessionId] ?? [] : []}
                      turnClocks={props.appStore.state.turnClocks}
                      turnLoads={props.appStore.state.turnLoads}
                      tail={viewport.presentsLiveTail() ? transcriptTail : []}
                    />
                  </ChatDestinationScope>
                </NetworkActionContext.Provider>
              </div>
            </div>
            </div>
            <div
              class="den-chat-stream-edge den-chat-stream-edge--top"
              aria-hidden="true"
            />
            <div
              class="den-chat-stream-edge den-chat-stream-edge--bottom"
              aria-hidden="true"
            />
            <TranscriptDayChip />
            <StreamScrollJump />
          </div>
  );
}
