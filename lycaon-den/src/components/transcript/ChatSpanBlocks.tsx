import type { PendingSend } from "../../chat/send/pending-sends.ts";
import { createEffect, createSignal, onCleanup } from "solid-js";
import type { AppStore } from "../../store/app-state-model.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { ChatSpanBlock } from "../../chat/workflow/workflow-spans.ts";
import { SessionTranscript, type TranscriptTailSlot } from "./SessionTranscript.tsx";
import { TurnLoadIndexProvider } from "../tool/turn-load-context.tsx";
import type { MessageRecoveryHandlers } from "./UserBubble.tsx";
import type { ChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import {
  advanceTranscriptAnnouncement,
  type TranscriptAnnouncementState,
} from "../../chat/transcript/presentation/transcript-live-announcement.ts";
import {
  bindFindableView,
} from "../../find/use-findable-view.ts";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";

type Props = {
  blocks: ChatSpanBlock[];
  messages: import("../../api/types.ts").Message[];
  sessionId: string;
  workers: import("../../api/types.ts").WorkerTask[];
  workerTranscripts?: import("../../chat/worker/worker-transcript.ts").WorkerTranscriptCache;
  onOpenWorker?: (workerId: string, opts?: import("../../chat/worker/workers-model.ts").OpenWorkerOptions) => void;
  checkpointClient?: LycaonClient | null;
  checkpointAppStore?: AppStore;
  projectDir?: string;
  visibleTurnActive: boolean;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  onExploreInSearch?: (query: string) => void;
  blueprint?: ChatBlueprintState;
  onBlueprintRevise?: (blueprintPath: string, text: string) => void;
  activePendingPhaseId?: string | null;
  recovery?: MessageRecoveryHandlers;
  /** Visible user turn clocks keyed by opening message id. */
  turnClocks?: Readonly<Record<string, import("../../api/types.ts").TurnClock>>;
  /** Decision-engine receipts keyed by opening message id. */
  turnLoads?: Readonly<Record<string, readonly import("../../api/types.ts").TurnLoad[]>>;
  surfaceActive?: () => boolean;
  pendingSends?: readonly PendingSend[];
  /** End-of-transcript surfaces — seated by the row flow, not beside it. */
  tail?: readonly TranscriptTailSlot[];
};

export function ChatSpanBlocks(props: Props) {
  const viewport = useTranscriptViewport();
  if (!viewport) {
    throw new Error("Chat transcript requires a viewport controller.");
  }
  const [announcement, setAnnouncement] = createSignal("");
  const [transcriptRoot, setTranscriptRoot] = createSignal<HTMLElement | null>(null);
  let announcementState: TranscriptAnnouncementState | undefined;
  let announcementEpoch = 0;

  createEffect(() => {
    const next = advanceTranscriptAnnouncement(
      announcementState,
      props.sessionId,
      props.messages,
    );
    announcementState = next.state;
    if (!next.announcement) {
      announcementEpoch += 1;
      setAnnouncement("");
      return;
    }
    const epoch = ++announcementEpoch;
    setAnnouncement("");
    queueMicrotask(() => {
      if (epoch === announcementEpoch) setAnnouncement(next.announcement ?? "");
    });
  });
  onCleanup(() => {
    announcementEpoch += 1;
  });

  bindFindableView({
    id: "session-transcript",
    root: transcriptRoot,
    primary: () => props.surfaceActive?.() ?? true,
    scrollMatchIntoView: (match) => {
      const node = match.range.startContainer;
      const element = node instanceof HTMLElement ? node : node.parentElement;
      if (element) viewport.ensureVisible(element);
    },
  });

  return (
    <section
      class="den-chat-transcript-log"
      role="log"
      aria-live="polite"
      aria-relevant="additions"
      aria-atomic="false"
      aria-label="Conversation"
      data-testid="conversation-log"
    >
      <div class="den-chat-transcript-visual" aria-live="off" ref={setTranscriptRoot}>
        <TurnLoadIndexProvider messages={() => props.messages} turnLoads={() => props.turnLoads}>
          <SessionTranscript
            messages={props.messages}
            sessionId={props.sessionId}
            workers={props.workers}
            workerTranscripts={props.workerTranscripts}
            onOpenWorker={props.onOpenWorker}
            transcriptSpans={props.blocks}
            visibleTurnActive={props.visibleTurnActive}
            checkpointClient={props.checkpointClient}
            checkpointAppStore={props.checkpointAppStore}
            projectDir={props.projectDir}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
            onExploreInSearch={props.onExploreInSearch}
            blueprint={props.blueprint}
            onBlueprintRevise={props.onBlueprintRevise}
            activePendingPhaseId={props.activePendingPhaseId}
            recovery={props.recovery}
            pendingSends={props.pendingSends}
            turnClocks={props.turnClocks}
            turnLoads={props.turnLoads}
            tail={props.tail}
          />
        </TurnLoadIndexProvider>
      </div>
      <span
        class="den-visually-hidden"
        data-testid="transcript-live-announcement"
      >
        {announcement()}
      </span>
    </section>
  );
}
