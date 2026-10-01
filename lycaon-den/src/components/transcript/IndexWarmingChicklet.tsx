import { Show } from "solid-js";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import {
  indexWarmingChickletTitle,
  indexWarmingSkipNote,
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
  const { key: disclosureKey, open, onToggle, onSummaryClick } = useTranscriptDisclosure(() =>
    props.entryKey ? transcriptDisclosureKey.indexWarming(props.entryKey) : undefined,
  );
  return (
    <details
      ref={bindTranscriptEntry}
      class="den-tool-part-card den-transcript-disclosure-card den-tool-part"
      data-testid="index-warming-chicklet"
      data-layout={props.layout}
      data-status="done"
      data-disclosure-key={disclosureKey}
      open={open()}
      onToggle={onToggle}
    >
      <TranscriptChickletSummary onClick={onSummaryClick}>
        <span
          class="den-tool-part-status-dot"
          data-status="done"
          aria-hidden="true"
        />
        <span class="den-tool-part-name">{indexWarmingToolName}</span>
        <span class="den-tool-part-title">
          {indexWarmingChickletTitle(props.meta)}
        </span>
      </TranscriptChickletSummary>
      <div class="den-tool-part-card-body den-tool-part-body">
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
      </div>
    </details>
  );
}
