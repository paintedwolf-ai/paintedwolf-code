import { Show, createSignal, onCleanup, type JSX } from "solid-js";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import {
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  useTranscriptDisclosure,
} from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { useActivitySpanAccordion } from "../../chat/tool/activity-span-accordion.ts";
import {
  indexWarmingChickletTitle,
  indexWarmingSkipNote,
  indexWarmingStatsLine,
  indexWarmingTierDetail,
  indexWarmingToolName,
  indexWarmingTopicNote,
  indexWarmingTriggerDetail,
} from "../../chat/transcript/projection/index-warming-copy.ts";
import type { IndexWarmingMeta } from "../../api/types.ts";

import { TranscriptChickletSummary } from "./TranscriptChickletSummary.tsx";

type Props = {
  meta: IndexWarmingMeta;
  layout: TranscriptLayout;
  sessionId?: string | null;
  entryKey?: string;
};

export function IndexWarmingChicklet(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.entryKey
      ? { sessionId: props.sessionId ?? undefined, entryKey: props.entryKey }
      : undefined,
  );
  const disclosure = useTranscriptDisclosure(() =>
    props.entryKey ? transcriptDisclosureKey.indexWarming(props.entryKey) : undefined,
  );
  const outcome = () => (props.meta.skip_reason ? "Skipped" : "Finished");
  const [shellEl, setShellEl] = createSignal<HTMLElement | null>(null);
  const [summaryEl, setSummaryEl] = createSignal<HTMLElement | null>(null);
  // Activity-span membership and the entry key stay fixed for this mount.
  const accordion = props.entryKey ? useActivitySpanAccordion() : undefined;
  if (accordion && props.entryKey) {
    const key = props.entryKey;
    onCleanup(accordion.register({
      key,
      disclosureKey: transcriptDisclosureKey.indexWarming(key),
      shell: shellEl,
      header: summaryEl,
      applyOpen: disclosure.applyOpen,
    }));
  }
  const onSummaryClick: JSX.EventHandler<HTMLButtonElement, MouseEvent> = (event) => {
    if (accordion && props.entryKey) accordion.toggle(props.entryKey);
    else disclosure.onToggleClick(event);
  };
  return (
    <TranscriptDisclosureShell
      disclosure={disclosure}
      ref={(el) => {
        bindTranscriptEntry(el);
        setShellEl(el);
      }}
      class="den-tool-part-card den-transcript-disclosure-card den-tool-part"
      data-testid="index-warming-chicklet"
      data-layout={props.layout}
      data-status="done"
      data-accordion-row={accordion ? "" : undefined}
    >
      <TranscriptChickletSummary
        disclosure={disclosure}
        ref={setSummaryEl}
        onClick={onSummaryClick}
        label={[indexWarmingToolName, indexWarmingChickletTitle(props.meta), outcome()].join(", ")}
        accordion={!!accordion}
      >
        <span
          class="den-tool-part-status-dot"
          data-status="done"
          aria-hidden="true"
        />
        <span class="den-tool-part-name">{indexWarmingToolName}</span>
        <span data-testid="tool-part-completion" class="den-tool-part-outcome">{outcome()}</span>
      </TranscriptChickletSummary>
      <TranscriptDisclosurePanel disclosure={disclosure} class="den-tool-part-card-body den-tool-part-body">
        <Show when={indexWarmingStatsLine(props.meta)}>
          {(stats) => <p class="den-tool-part-card-note">{stats()}</p>}
        </Show>
        <p class="den-tool-part-card-note">
          {indexWarmingTriggerDetail(props.meta.trigger)}
        </p>
        <Show when={indexWarmingTierDetail(props.meta.tier)}>
          {(tierDetail) => (
            <p class="den-tool-part-card-note">{tierDetail()}</p>
          )}
        </Show>
        <Show when={props.meta.topic}>
          {(topic) => (
            <p class="den-tool-part-card-note">
              {indexWarmingTopicNote(topic(), props.meta.trigger)}
            </p>
          )}
        </Show>
        <Show when={props.meta.hosts?.length}>
          <p class="den-tool-part-card-note">
            Hosts: {(props.meta.hosts ?? []).join(", ")}
          </p>
        </Show>
        <Show when={props.meta.duration_ms}>
          <p class="den-tool-part-card-note">
            {Math.round((props.meta.duration_ms ?? 0) / 100) / 10}s
          </p>
        </Show>
        <Show when={props.meta.skip_reason}>
          {(reason) => (
            <p class="den-tool-part-card-note">
              {indexWarmingSkipNote(reason())}
            </p>
          )}
        </Show>
      </TranscriptDisclosurePanel>
    </TranscriptDisclosureShell>
  );
}
