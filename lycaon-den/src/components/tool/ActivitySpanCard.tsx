import { Match, Show, Switch, createEffect, createMemo, createSignal, onCleanup, type JSX } from "solid-js";
import { activitySpanEnterFadeKey, useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import {
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  TranscriptDisclosureToggle,
  useTranscriptDisclosure,
} from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey, type TranscriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { pinnedRevealTool } from "../../chat/transcript/presentation/transcript-reveal.ts";
import { subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { createActivitySpanAccordion, ActivitySpanAccordionProvider } from "../../chat/tool/activity-span-accordion.ts";
import { activitySpanSummary } from "../../chat/tool/activity-span-summary.ts";
import { activitySpanEntryKey, type ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { bindFindRevealHost } from "../../find/use-findable-view.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import { IndexWarmingChicklet } from "../transcript/IndexWarmingChicklet.tsx";
import { TurnLoadCard } from "./TurnLoadCard.tsx";
import { VirtualCardList } from "../primitives/VirtualCardList.tsx";
import { searchToolPart, TOOL_CONTENT_REVEAL_EVENT, toolPartFindCorpus } from "../../chat/tool/tool-part-find.ts";
import { findController } from "../../find/find-controller.ts";
import { webkitAtspiWorkaroundActive } from "../../platform/desktop/webkit-atspi-workaround.ts";
import type { VirtualListMatch } from "../../find/virtual-list-find.ts";
import { findMatchingActivityToolEntry, formatActivityCountChip } from "./CommandActivitySpan.tsx";

export type ActivitySpanCardProps = {
  label: string;
  sequence?: string;
  entries: readonly ActivitySpanEntry[];
  layout: TranscriptLayout;
  sessionId?: string;
  entryKey?: string;
  disclosureKey?: TranscriptDisclosureKey;
  chrome?: "card" | "section";
  toolCallIds?: string;
  waitCount?: number;
  running?: boolean;
  renderToolEntry: (part: () => ToolPartView) => JSX.Element;
};

function ActivitySpanEntryRow(props: {
  entry: () => ActivitySpanEntry;
  layout: TranscriptLayout;
  sessionId?: string;
  renderToolEntry: (part: () => ToolPartView) => JSX.Element;
}) {
  return (
    <Switch
      fallback={
        <IndexWarmingChicklet
          meta={(() => {
            const entry = props.entry();
            if (entry.kind !== "index_warming") throw new Error("activity span entry kind flipped under stable key");
            return entry.meta;
          })()}
          layout={props.layout}
          sessionId={props.sessionId}
          entryKey={(() => {
            const entry = props.entry();
            return entry.kind === "index_warming" ? entry.key : "";
          })()}
        />
      }
    >
      <Match when={props.entry().kind === "tool"}>
        {props.renderToolEntry(() => {
          const entry = props.entry();
          if (entry.kind !== "tool") throw new Error("activity span entry kind flipped under stable key");
          return entry.part;
        })}
      </Match>
      <Match when={props.entry().kind === "turn_load"}>
        <TurnLoadCard
          row={(() => {
            const entry = props.entry();
            if (entry.kind !== "turn_load") throw new Error("activity span entry kind flipped under stable key");
            return entry.row;
          })()}
          layout={props.layout}
          sessionId={props.sessionId}
        />
      </Match>
    </Switch>
  );
}

export function ActivitySpanCard(props: ActivitySpanCardProps) {
  const entries = () => props.entries;
  const asSection = () => props.chrome === "section";
  const label = () => formatSentenceCase(props.label);
  const { bindTranscriptEntry } = useTranscriptEntry(() => {
    const key = props.entryKey?.trim();
    if (!key) return { sessionId: props.sessionId, skipFade: asSection() };
    return { sessionId: props.sessionId, entryKey: activitySpanEnterFadeKey(key), skipFade: asSection() };
  });
  const disclosure = useTranscriptDisclosure(() => {
    if (props.disclosureKey) return props.disclosureKey;
    const key = props.entryKey?.trim();
    return key ? transcriptDisclosureKey.activitySpan(key) : undefined;
  });
  const { open, onToggleClick, revealTemporarily } = disclosure;
  const accordion = createActivitySpanAccordion();
  const [findToolKey, setFindToolKey] = createSignal<string>();
  let revealRow: ((key: string) => void) | undefined;
  let restoreFind: (() => void) | undefined;

  const pinnedEntryKey = createMemo(() => {
    const pinned = pinnedRevealTool();
    if (!pinned || (props.sessionId && pinned.sessionId !== props.sessionId)) return undefined;
    return findMatchingActivityToolEntry(entries(), pinned.toolCallId, pinned.path)?.part.id;
  });

  createEffect(() => {
    const key = pinnedEntryKey();
    if (key && open()) revealRow?.(key);
  });

  const revealTool = (toolCallId: string, match: VirtualListMatch) => {
    const entry = findMatchingActivityToolEntry(entries(), toolCallId);
    if (!entry) return;
    restoreFind?.();
    restoreFind = revealTemporarily();
    setFindToolKey(entry.part.id);
    revealRow?.(entry.part.id);
    queueMicrotask(() =>
      hostEl()
        ?.querySelector(`[data-tool-part-id="${CSS.escape(entry.part.id)}"]`)
        ?.dispatchEvent(new CustomEvent(TOOL_CONTENT_REVEAL_EVENT, { detail: match })),
    );
  };
  const [hostEl, setHostEl] = createSignal<HTMLElement | null>(null);
  createEffect(() => {
    void findController.query();
    void findController.isOpen();
    restoreFind?.();
    restoreFind = undefined;
    setFindToolKey(undefined);
  });
  onCleanup(() => restoreFind?.());
  createEffect(() => {
    const host = hostEl();
    if (!host) return;
    const reveal = (event: Event) => {
      const match = (event as CustomEvent<VirtualListMatch>).detail;
      if (match.toolCallId) revealTool(match.toolCallId, match);
    };
    host.addEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal);
    onCleanup(() => host.removeEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal));
  });
  const revealId = () => `activity-span-reveal:${disclosure.key ?? props.label}`;
  const collapsedToolCorpus = () =>
    entries().flatMap((entry) => (entry.kind === "tool" ? [toolPartFindCorpus(entry.part)] : [])).join("\n");
  bindFindRevealHost({
    id: revealId(),
    hostEl,
    isCollapsed: () => !open(),
    revealForFind: revealTemporarily,
    collapsedCorpus: () => (webkitAtspiWorkaroundActive() ? collapsedToolCorpus() : ""),
    collapsedCorpusAnchor: hostEl,
  });
  const count = () => entries().length;
  const countChip = () => formatActivityCountChip(entries(), props.waitCount ?? 0);
  const [processRevision, setProcessRevision] = createSignal(0);
  onCleanup(
    subscribeBackgroundProcessStore(
      () => setProcessRevision((n) => n + 1),
      () => ({ sessionId: props.sessionId, statusOnly: true }),
    ),
  );
  const summary = createMemo(() => {
    processRevision();
    return activitySpanSummary(entries(), {
      sessionId: props.sessionId,
      running: props.running,
      verboseMode: verboseModePref(),
    });
  });

  const singletonToolKey = () => {
    const entry = entries()[0];
    return count() === 1 && entry?.kind === "tool" ? entry.part.id : null;
  };
  const handleSummaryClick: JSX.EventHandler<HTMLButtonElement, MouseEvent> = (event) => {
    const expanding = !open();
    onToggleClick(event);
    const toolKey = singletonToolKey();
    if (expanding && toolKey) accordion.open(toolKey);
  };

  return (
    <TranscriptDisclosureShell
      disclosure={disclosure}
      ref={(el) => {
        bindTranscriptEntry(el);
        setHostEl(el);
      }}
      class={
        asSection()
          ? "den-activity-span den-activity-span--section den-transcript-disclosure-card"
          : "den-activity-span den-transcript-card den-transcript-disclosure-card"
      }
      data-testid="activity-span-card"
      data-chrome={asSection() ? "section" : "card"}
      data-tool={props.label}
      data-count={count()}
      data-layout={props.layout}
      data-status={summary().status}
      data-tool-call-ids={props.toolCallIds || undefined}
    >
      <TranscriptDisclosureToggle
        disclosure={disclosure}
        onClick={handleSummaryClick}
        aria-label={`${label()}, ${summary().status}, ${count()} ${count() === 1 ? "action" : "actions"}${
          (props.waitCount ?? 0) > 0 ? `, ${props.waitCount} ${props.waitCount === 1 ? "wait" : "waits"}` : ""
        }${summary().failureCount > 0 ? `, ${summary().failureCount} failed` : ""}`}
      >
        <span class={asSection() ? "den-activity-span-chicklet den-activity-span-chicklet--section" : "den-activity-span-chicklet"}>
          <span class="den-activity-span-chip">
            <span class="den-tool-part-status-dot" data-status={summary().status} aria-hidden="true" />
            <span class="den-activity-span-name">{label()}</span>
            <Show when={countChip()}>
              {(chip) => <span class="den-activity-span-count" data-testid="activity-span-count">{chip()}</span>}
            </Show>
            <Show when={summary().failureCount > 0}>
              <span class="den-activity-span-failed" data-testid="activity-span-failed">
                {summary().failureCount} failed
              </span>
            </Show>
          </span>
          <Show when={asSection() && props.sequence !== undefined}>
            <span class="den-activity-span-hint den-activity-span-hint--inline" data-tip={props.sequence || undefined} data-tip-when-clipped="">
              {props.sequence}
            </span>
          </Show>
          <span class="den-tool-chicklet-caret" aria-hidden="true" />
          <Show when={!asSection() && props.sequence !== undefined}>
            <span class="den-activity-span-hint" data-tip={props.sequence || undefined} data-tip-when-clipped="">
              {props.sequence}
            </span>
          </Show>
        </span>
      </TranscriptDisclosureToggle>
      <TranscriptDisclosurePanel disclosure={disclosure} class="den-activity-span-items">
        <ActivitySpanAccordionProvider value={accordion}>
          <VirtualCardList
            threshold={100}
            scroll="ancestor"
            keepReadingPosition
            items={entries()}
            keyOf={activitySpanEntryKey}
            label="Actions"
            pinnedKeys={[accordion.activeKey(), findToolKey(), pinnedEntryKey()].filter((key): key is string => !!key)}
            onReveal={(value) => {
              revealRow = value;
            }}
            search={(entry, query, sensitive, signal) =>
              entry.kind === "tool" ? searchToolPart(entry.part, props.sessionId, query, sensitive, signal) : Promise.resolve([])
            }
            onFindReveal={(entry, match) => {
              if (entry.kind !== "tool") return;
              revealTool(match.toolCallId ?? entry.part.toolCallId, match);
            }}
          >
            {(entry) => (
              <div class="den-activity-span-item" data-activity-entry-key={activitySpanEntryKey(entry())}>
                <ActivitySpanEntryRow
                  entry={entry}
                  layout={props.layout}
                  sessionId={props.sessionId}
                  renderToolEntry={props.renderToolEntry}
                />
              </div>
            )}
          </VirtualCardList>
        </ActivitySpanAccordionProvider>
      </TranscriptDisclosurePanel>
    </TranscriptDisclosureShell>
  );
}
