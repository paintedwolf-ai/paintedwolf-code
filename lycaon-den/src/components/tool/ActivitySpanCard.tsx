import { Index, Match, Show, Switch, createEffect, createMemo, createSignal, onCleanup, type JSX } from "solid-js";
import {
  activitySpanEnterFadeKey,
  useTranscriptEntry,
} from "../../chat/transcript/presentation/transcript-entry.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import {
  createActivitySpanAccordion,
  ActivitySpanAccordionProvider,
} from "../../chat/tool/activity-span-accordion.ts";
import {
  formatActivitySpanComposition,
  activitySpanComposition,
  activitySpanFailureCount,
  activitySpanHint,
  activitySpanStatus,
} from "../../chat/tool/activity-span-model.ts";
import type { ActivitySpanEntry } from "../../chat/transcript/projection/transcript-item-model.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { bindFindRevealHost } from "../../find/use-findable-view.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { IndexWarmingChicklet } from "../transcript/IndexWarmingChicklet.tsx";
import { TurnLoadCard } from "./TurnLoadCard.tsx";
import { VirtualCardList } from "../primitives/VirtualCardList.tsx";
import { searchToolPart, TOOL_CONTENT_REVEAL_EVENT } from "../../chat/tool/tool-part-find.ts";
import { findController } from "../../find/find-controller.ts";
import type { VirtualListMatch } from "../../find/virtual-list-find.ts";

type Props = {
  label: string;
  entries: readonly ActivitySpanEntry[];
  layout: TranscriptLayout;
  sessionId?: string;
  entryKey?: string;
  renderToolEntry: (part: () => ToolPartView) => JSX.Element;
};

function entryLeadKey(entry: ActivitySpanEntry): string {
  return entry.kind === "tool" ? entry.part.id : entry.key;
}

function ActivitySpanToolEntryRow(props: {
  entry: () => ActivitySpanEntry;
  renderToolEntry: (part: () => ToolPartView) => JSX.Element;
}) {
  const part = (): ToolPartView => {
    const entry = props.entry();
    if (entry.kind !== "tool") {
      throw new Error("activity span entry kind flipped under stable key");
    }
    return entry.part;
  };
  return props.renderToolEntry(part);
}

function ActivitySpanWarmingEntryRow(props: {
  entry: () => ActivitySpanEntry;
  layout: TranscriptLayout;
  sessionId?: string;
}) {
  const warming = () => {
    const entry = props.entry();
    if (entry.kind !== "index_warming") {
      throw new Error("activity span entry kind flipped under stable key");
    }
    return entry;
  };
  return (
    <IndexWarmingChicklet
      meta={warming().meta}
      layout={props.layout}
      sessionId={props.sessionId}
      entryKey={warming().key}
    />
  );
}

function ActivitySpanTurnLoadEntryRow(props: {
  entry: () => ActivitySpanEntry;
  layout: TranscriptLayout;
  sessionId?: string;
}) {
  const row = () => {
    const entry = props.entry();
    if (entry.kind !== "turn_load") {
      throw new Error("activity span entry kind flipped under stable key");
    }
    return entry.row;
  };
  return <TurnLoadCard row={row()} layout={props.layout} sessionId={props.sessionId} />;
}

function ActivitySpanEntryRow(props: {
  entry: () => ActivitySpanEntry;
  layout: TranscriptLayout;
  sessionId?: string;
  renderToolEntry: (part: () => ToolPartView) => JSX.Element;
}) {
  // Stable row keys preserve card state across stream updates.
  return (
    <Switch
      fallback={
        <ActivitySpanWarmingEntryRow
          entry={props.entry}
          layout={props.layout}
          sessionId={props.sessionId}
        />
      }
    >
      <Match when={props.entry().kind === "tool"}>
        <ActivitySpanToolEntryRow
          entry={props.entry}
          renderToolEntry={props.renderToolEntry}
        />
      </Match>
      <Match when={props.entry().kind === "turn_load"}>
        <ActivitySpanTurnLoadEntryRow
          entry={props.entry}
          layout={props.layout}
          sessionId={props.sessionId}
        />
      </Match>
    </Switch>
  );
}

function ActivityFailureChip(props: { failed: number }) {
  return (
    <Show when={props.failed > 0}>
      <span class="den-activity-span-failed" data-testid="activity-span-failed">
        {props.failed} failed
      </span>
    </Show>
  );
}

export function ActivitySpanCard(props: Props) {
  const label = () => formatSentenceCase(props.label);
  const { bindTranscriptEntry } = useTranscriptEntry(() => {
    const key = props.entryKey?.trim();
    if (!key) return { sessionId: props.sessionId };
    return {
      sessionId: props.sessionId,
      entryKey: activitySpanEnterFadeKey(key),
    };
  });
  const {
    key: disclosureKey,
    open,
    onToggle,
    onSummaryClick,
    revealTemporarily,
  } = useTranscriptDisclosure(() => {
    const key = props.entryKey?.trim();
    return key ? transcriptDisclosureKey.activitySpan(key) : undefined;
  });
  const accordion = createActivitySpanAccordion();
  const [findToolKey, setFindToolKey] = createSignal<string>();
  let revealRow: ((key: string) => void) | undefined;
  let restoreFind: (() => void) | undefined;
  const revealTool = (toolCallId: string, match: VirtualListMatch) => {
    const entry = props.entries.find(entry => entry.kind === "tool" && entry.part.toolCallId === toolCallId);
    if (entry?.kind !== "tool") return;
    restoreFind?.(); restoreFind = revealTemporarily();
    setFindToolKey(entry.part.id); revealRow?.(entry.part.id);
    queueMicrotask(() => hostEl()?.querySelector(`[data-tool-call-id="${CSS.escape(toolCallId)}"]`)?.dispatchEvent(new CustomEvent(TOOL_CONTENT_REVEAL_EVENT, { detail: match })));
  };
  const [hostEl, setHostEl] = createSignal<HTMLElement | null>(null);
  createEffect(() => {
    void findController.query(); void findController.isOpen();
    restoreFind?.(); restoreFind = undefined; setFindToolKey(undefined);
  });
  onCleanup(() => restoreFind?.());
  createEffect(() => {
    const host = hostEl(); if (!host) return;
    const reveal = (event: Event) => { const match = (event as CustomEvent<VirtualListMatch>).detail; if (match.toolCallId) revealTool(match.toolCallId, match); };
    host.addEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal);
    onCleanup(() => host.removeEventListener(TOOL_CONTENT_REVEAL_EVENT, reveal));
  });
  const revealId = () => `activity-span-reveal:${disclosureKey ?? props.label}`;
  bindFindRevealHost({
    id: revealId(),
    hostEl,
    isCollapsed: () => !open(),
    revealForFind: revealTemporarily,
  });
  const count = () => props.entries.length;
  const [processRevision, setProcessRevision] = createSignal(0);
  onCleanup(
    subscribeBackgroundProcessStore(() => {
      setProcessRevision((n) => n + 1);
    }, () => ({ sessionId: props.sessionId, statusOnly: true })),
  );
  // Process events refresh status and failure counts.
  const statusAttr = () => {
    processRevision();
    const status = activitySpanStatus(props.entries, props.sessionId);
    return status === "completed" ? "done" : status;
  };
  const failed = () => {
    processRevision();
    return activitySpanFailureCount(props.entries, props.sessionId);
  };

  const summary = createMemo(() => {
    const composition = activitySpanComposition(props.entries);
    const hint = activitySpanHint(props.entries, props.label, props.sessionId);
    return {
      composition,
      detail: hint || formatActivitySpanComposition(composition),
    };
  });
  const singletonToolKey = () => {
    const entry = props.entries[0];
    return count() === 1 && entry?.kind === "tool" ? entry.part.id : null;
  };
  const handleSummaryClick: JSX.EventHandler<HTMLElement, MouseEvent> = (
    event,
  ) => {
    const expanding = !open();
    onSummaryClick(event);
    const toolKey = singletonToolKey();
    if (expanding && toolKey) accordion.open(toolKey);
  };

  return (
    <details
      ref={(el) => {
        bindTranscriptEntry(el);
        setHostEl(el);
      }}
      class="den-activity-span den-transcript-disclosure-card"
      data-testid="activity-span-card"
      data-tool={props.label}
      data-count={count()}
      data-layout={props.layout}
      data-status={statusAttr()}
      data-disclosure-key={disclosureKey}
      open={open()}
      onToggle={onToggle}
    >
      <summary
        onClick={handleSummaryClick}
        aria-label={`${label()}, ${statusAttr()}, ${count()} ${count() === 1 ? "action" : "actions"}${
          failed() > 0 ? `, ${failed()} failed` : ""
        }`}
      >
        <span class="den-activity-span-chicklet">
          <span class="den-activity-span-chip">
            <span
              class="den-tool-part-status-dot"
              data-status={statusAttr()}
              aria-hidden="true"
            />
            <span class="den-activity-span-name">{label()}</span>
            <Show when={count() > 1}>
              <span class="den-activity-span-count">×{count()}</span>
            </Show>
            <ActivityFailureChip failed={failed()} />
          </span>
          <span class="den-tool-chicklet-caret" aria-hidden="true" />
          <span class="den-activity-span-hint">{summary().detail}</span>
        </span>
      </summary>
      <div class="den-activity-span-items">
        <span
          class="den-activity-span-composition"
          aria-label={formatActivitySpanComposition(summary().composition)}
        >
          <Index each={summary().composition}>
            {(part) => (
              <span class="den-activity-span-cmd">
                <span class="den-activity-span-cmd-name">
                  {formatSentenceCase(part().label)}
                </span>
                <Show when={part().count > 1}>
                  <span class="den-activity-span-cmd-count">×{part().count}</span>
                </Show>
              </span>
            )}
          </Index>
        </span>
        <ActivitySpanAccordionProvider value={accordion}>
          <VirtualCardList threshold={100} scroll="ancestor" items={props.entries} keyOf={entryLeadKey} label="Actions"
            pinnedKeys={[accordion.activeKey(), findToolKey()].filter((key): key is string => !!key)}
            onReveal={value => { revealRow = value; }}
            search={(entry, query, sensitive, signal) => entry.kind === "tool" ? searchToolPart(entry.part, props.sessionId, query, sensitive, signal) : Promise.resolve([])}
            onFindReveal={(entry, match) => {
              if (entry.kind !== "tool") return;
              revealTool(entry.part.toolCallId, match);
            }}>
            {(entry) => (
              <div class="den-activity-span-item">
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
      </div>
    </details>
  );
}
