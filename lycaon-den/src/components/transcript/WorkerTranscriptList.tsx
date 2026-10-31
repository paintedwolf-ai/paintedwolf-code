import { createMemo, onCleanup, type Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { Message, WorkerTask } from "../../api/types.ts";
import { TOOL_CONTENT_REVEAL_EVENT } from "../../chat/tool/tool-part-find.ts";
import type { TranscriptSeam } from "../../chat/transcript/layout/transcript-spacing.ts";
import {
  createTranscriptDisclosureStore,
  TranscriptDisclosureProvider,
} from "../../chat/transcript/presentation/disclosure-state.tsx";
import type { reviewTurnsByAssistant } from "../../chat/transcript/presentation/review-turns.ts";
import type { DisplayTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import { ArtifactDedupProvider } from "../../chat/visual/artifact-dedup-context.tsx";
import type { CanonicalArtifactPlacements } from "../../chat/visual/visual-artifact-canonical.ts";
import {
  searchWorkerItem,
  workerItemHasExternalFind,
} from "../../chat/worker/worker-item-find.ts";
import type { WorkerTranscriptCache } from "../../chat/worker/worker-transcript.ts";
import type { OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { CitationExploreContext } from "../citation/CitationGroundingPanel.tsx";
import { VirtualCardList } from "../primitives/VirtualCardList.tsx";
import { TranscriptItemRow } from "./TranscriptItemRow.tsx";
import type { MessageRecoveryHandlers } from "./UserBubble.tsx";

export type WorkerTranscriptListProps = {
  items: DisplayTranscriptItem[];
  artifactPlacements: Accessor<CanonicalArtifactPlacements>;
  workerListRef?: (element: HTMLElement | undefined) => void;
  sessionId?: string | null;
  branchJobId?: string;
  seamForItem: (key: string) => TranscriptSeam | undefined;
  taskMatches: Accessor<ReadonlyMap<string, WorkerTask>>;
  workers: WorkerTask[];
  messages: Message[];
  messageById: Accessor<Map<string, Message>>;
  reviewTurns: Accessor<ReturnType<typeof reviewTurnsByAssistant>>;
  workerTranscripts?: WorkerTranscriptCache;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
  checkpointClient?: LycaonClient | null;
  checkpointAppStore?: AppStore;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  projectDir?: string;
  exploreContext: CitationExploreContext;
  onExploreInSearch?: (query: string) => void;
  activePendingPhaseId?: string | null;
  visibleTurnActive: boolean;
  recovery?: MessageRecoveryHandlers;
};

export function WorkerTranscriptList(props: WorkerTranscriptListProps) {
  const workerDisclosures = createMemo(() => {
    void props.sessionId;
    void props.branchJobId;
    return createTranscriptDisclosureStore();
  });

  return (
    <TranscriptDisclosureProvider value={workerDisclosures()}>
      <ArtifactDedupProvider placements={props.artifactPlacements}>
        <div
          role="list"
          class="den-worker-transcript"
          data-testid="message-stream"
          ref={(element) => {
            props.workerListRef?.(element);
            onCleanup(() => props.workerListRef?.(undefined));
          }}
        >
          <VirtualCardList
            items={props.items}
            keyOf={(item) => item.key}
            label="Worker activity"
            estimateSize={120}
            scroll="ancestor"
            keepReadingPosition
            externalFind={workerItemHasExternalFind}
            search={(item, query, sensitive, signal) =>
              searchWorkerItem(
                item,
                props.sessionId ?? undefined,
                query,
                sensitive,
                signal,
              )
            }
            onFindReveal={(item, match) => {
              if (!match.toolCallId) return;
              const row = document.querySelector<HTMLElement>(
                `[data-worker-item-key="${CSS.escape(item.key)}"]`,
              );
              const tool =
                item.kind === "activity_span"
                  ? row?.querySelector<HTMLElement>(".den-activity-span")
                  : row?.querySelector<HTMLElement>(
                      `[data-tool-call-id="${CSS.escape(match.toolCallId)}"]`,
                    );
              if (!tool) return;
              tool.dispatchEvent(
                new CustomEvent(TOOL_CONTENT_REVEAL_EVENT, { detail: match }),
              );
            }}
          >
            {(item) => (
              <div
                role="listitem"
                class="den-seam"
                data-worker-item-key={item().key}
                data-seam={props.seamForItem(item().key)}
              >
                <TranscriptItemRow
                  item={item}
                  taskMatches={props.taskMatches}
                  workers={props.workers}
                  messages={props.messages}
                  messageById={props.messageById}
                  reviewTurns={props.reviewTurns}
                  workerTranscripts={props.workerTranscripts}
                  onOpenWorker={props.onOpenWorker}
                  layout="worker"
                  sessionId={props.sessionId}
                  checkpointClient={props.checkpointClient}
                  checkpointAppStore={props.checkpointAppStore}
                  projectId={props.projectId}
                  rootRefs={props.rootRefs}
                  branchJobId={props.branchJobId}
                  projectDir={props.projectDir}
                  exploreContext={props.exploreContext}
                  onExploreInSearch={props.onExploreInSearch}
                  activePendingPhaseId={props.activePendingPhaseId}
                  visibleTurnActive={props.visibleTurnActive}
                  recovery={props.recovery}
                />
              </div>
            )}
          </VirtualCardList>
        </div>
      </ArtifactDedupProvider>
    </TranscriptDisclosureProvider>
  );
}
