import { For, Match, Switch, createEffect, onCleanup } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ResolveProjectRoot } from "../../../api/project-path.ts";
import type { Message, WorkerTask } from "../../../api/types.ts";
import { useTranscriptViewport } from "../../../chat/stream/transcript-viewport.tsx";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { activitySpanEnterFadeKey, useTranscriptEntry } from "../../../chat/transcript/presentation/transcript-entry.ts";
import { revealChicklet } from "../../../chat/transcript/presentation/transcript-reveal.ts";
import type { ActivityGroupItem, ActivityGroupSection } from "../../../chat/transcript/projection/transcript-item-model.ts";
import type { WorkerTranscriptCache } from "../../../chat/worker/worker-transcript.ts";
import type { OpenWorkerOptions } from "../../../chat/worker/workers-model.ts";
import { observeSharedContentBox } from "../../../layout/shared-resize-observer.ts";
import { useNoticesOptional } from "../../../notices/notice-reporter.tsx";
import { scrollportMotionForViewport } from "../../../platform/scrolling/scrollport-motion.ts";
import type { AppStore } from "../../../store/app-state-model.ts";
import { CommandActivitySpan } from "../../tool/CommandActivitySpan.tsx";
import { TranscriptDiffGroup } from "../TranscriptDiffGroup.tsx";
import { ActivityToolEntry } from "../WorkerTranscriptRows.tsx";
import { ActivityGroupApprovals } from "./ActivityGroupApprovals.tsx";

type Props = {
  item: ActivityGroupItem;
  taskMatches: ReadonlyMap<string, WorkerTask>;
  sessionWorkers: WorkerTask[];
  appStore?: AppStore;
  messages: Message[];
  workerTranscripts?: WorkerTranscriptCache;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  branchJobId?: string;
  checkpointClient?: LycaonClient | null;
};

export function ActivityGroupCard(props: Props) {
  const notices = useNoticesOptional();
  const viewport = useTranscriptViewport();
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId ?? undefined,
    entryKey: activitySpanEnterFadeKey(props.item.key),
  }));

  let cardEl: HTMLElement | undefined;
  const sectionEls = new Map<ActivityGroupSection, HTMLElement>();
  let prevOffsets = new Map<ActivityGroupSection, number>();

  const reconcileSectionOffsets = () => {
    const root = cardEl;
    if (!root) return;
    const order = props.item.sectionOrder;
    const nextOffsets = new Map<ActivityGroupSection, number>();
    for (const section of order) {
      const el = sectionEls.get(section);
      if (el) nextOffsets.set(section, el.offsetTop);
    }

    const scroller = viewport?.stream();
    if (scroller && viewport && !viewport.following() && prevOffsets.size > 0 && order.length > 1) {
      const scrollerRect = scroller.getBoundingClientRect();
      const cardRect = root.getBoundingClientRect();
      const lineY = scrollerRect.top + scroller.clientTop;
      const readingOffsetInCard = lineY - cardRect.top;
      if (readingOffsetInCard > 0 && cardRect.bottom > lineY) {
        for (let idx = 1; idx < order.length; idx++) {
          const section = order[idx]!;
          const before = prevOffsets.get(section);
          const after = nextOffsets.get(section);
          if (before === undefined || after === undefined) continue;
          if (before <= readingOffsetInCard) {
            const delta = after - before;
            if (delta !== 0) {
              const currentOffset = scrollportMotionForViewport(scroller)?.offsetY() ?? scroller.scrollTop;
              viewport.shiftVirtualContent(delta, currentOffset);
            }
            break;
          }
        }
      }
    }
    prevOffsets = nextOffsets;
  };

  createEffect(() => {
    const order = props.item.sectionOrder;
    const cleanups: (() => void)[] = [];
    const root = cardEl;
    if (root) {
      root.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, reconcileSectionOffsets);
      cleanups.push(() => root.removeEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, reconcileSectionOffsets));
    }
    for (const section of order) {
      const el = sectionEls.get(section);
      if (el) cleanups.push(observeSharedContentBox(el, reconcileSectionOffsets));
    }
    reconcileSectionOffsets();
    onCleanup(() => {
      for (const stop of cleanups) stop();
    });
  });

  const showActivity = (toolCallId: string, path?: string) => {
    const sid = props.sessionId?.trim();
    if (!sid || !toolCallId.trim()) return;
    void revealChicklet(
      () => cardEl?.closest<HTMLElement>(".den-chat-stream") ?? document.querySelector<HTMLElement>(".den-chat-stream"),
      {
        sessionId: sid,
        anchor: {
          chicklet: "tool",
          anchorId: toolCallId.trim(),
          path,
          persistExpansion: true,
        },
      },
    );
  };

  return (
    <section
      ref={(el) => {
        cardEl = el;
        bindTranscriptEntry(el);
      }}
      class="den-activity-group den-transcript-card"
      data-testid="activity-group"
      data-sections={props.item.sectionOrder.join(",")}
    >
      <For each={props.item.sectionOrder}>
        {(section) => (
          <div
            ref={(el) => {
              sectionEls.set(section, el);
              onCleanup(() => {
                if (sectionEls.get(section) === el) sectionEls.delete(section);
              });
            }}
            class="den-activity-group-section"
            data-activity-group-section={section}
          >
            <Switch>
              <Match when={section === "activities"}>
                <CommandActivitySpan
                  chrome="section"
                  disclosureKey={transcriptDisclosureKey.activityGroupActivities(props.item.key)}
                  label={props.item.headline}
                  entries={props.item.entries}
                  layout="chat"
                  sessionId={props.sessionId ?? undefined}
                  entryKey={props.item.key}
                  renderToolEntry={(part) => (
                    <ActivityToolEntry
                      part={part()}
                      matchedWorker={props.taskMatches.get(part().id)}
                      sessionWorkers={props.sessionWorkers}
                      appStore={props.appStore}
                      messages={props.messages}
                      workerTranscripts={props.workerTranscripts}
                      onOpenWorker={props.onOpenWorker}
                      layout="chat"
                      sessionId={props.sessionId}
                      projectId={props.projectId}
                      rootRefs={props.rootRefs}
                      client={props.checkpointClient}
                    />
                  )}
                />
              </Match>
              <Match when={section === "approvals"}>
                <ActivityGroupApprovals
                  groupKey={props.item.key}
                  approvals={props.item.approvals}
                  sessionId={props.sessionId}
                  projectId={props.projectId}
                  client={props.checkpointClient}
                  onShowActivity={(toolCallId) => showActivity(toolCallId)}
                  onError={(err) => notices.reportError(err)}
                />
              </Match>
              <Match when={section === "files"}>
                <TranscriptDiffGroup
                  chrome="section"
                  disclosureKey={transcriptDisclosureKey.activityGroupFiles(props.item.key)}
                  folds={props.item.folds}
                  omitted={props.item.filesOmitted}
                  layout="chat"
                  sessionId={props.sessionId}
                  projectId={props.projectId}
                  rootRefs={props.rootRefs}
                  jobId={props.branchJobId}
                  entryKey={props.item.key}
                  onShowActivity={showActivity}
                />
              </Match>
            </Switch>
          </div>
        )}
      </For>
    </section>
  );
}
