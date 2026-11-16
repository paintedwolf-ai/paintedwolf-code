import { For, Show, createMemo } from "solid-js";
import type { ProgressChange, ProgressStep, ProgressUpdateSummary } from "../../api/types.ts";
import {
  PROGRESS_SNAPSHOT_TITLES,
  progressCompletedLabel,
  progressSnapshotCount,
  progressSnapshotSteps,
  progressUpdateStanding,
  type ProgressSnapshotKind,
} from "../../chat/progress/progress-model.ts";
import {
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  TranscriptDisclosureToggle,
  useTranscriptDisclosure,
} from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { ProgressStepLabel, ProgressStepRow, progressStateGlyph } from "../ProgressStepRow.tsx";

type Props = {
  kind: ProgressSnapshotKind;
  steps?: readonly ProgressStep[];
  changes?: readonly ProgressChange[];
  summary?: ProgressUpdateSummary;
  sessionId?: string;
  entryKey?: string;
};

const TEST_IDS: Record<ProgressSnapshotKind, string> = {
  created: "progress-strip-created",
  updated: "progress-strip-delta",
  completed: "progress-strip-completed",
};

function StepList(props: { steps: readonly ProgressStep[] }) {
  return (
    <ul class="progress-strip__list">
      <KeyedIndex each={props.steps} keyOf={(step) => step.label}>
        {(step) => <ProgressStepRow step={step()} />}
      </KeyedIndex>
    </ul>
  );
}

/**
 * A progress row in the transcript. The plan shows as a box; each later change and the
 * finished plan rest as one line that opens into the same box.
 */
export function ProgressSnapshot(props: Props) {
  // A rebuild that momentarily carries no steps keeps the rows it replaced.
  const steps = createMemo<ProgressStep[]>((held) => {
    const next = progressSnapshotSteps(props);
    return next.length > 0 ? next : held;
  }, []);
  const count = () => progressSnapshotCount(props, steps().length);
  const entryKey = () => props.entryKey?.trim();
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.sessionId && entryKey() ? { sessionId: props.sessionId, entryKey: entryKey() } : undefined,
  );
  const disclosure = useTranscriptDisclosure(() => {
    const key = entryKey();
    return key && props.kind !== "created" ? transcriptDisclosureKey.progressUpdate(key) : undefined;
  });
  const title = () => PROGRESS_SNAPSHOT_TITLES[props.kind];
  const restSteps = (): ProgressStep[] =>
    props.kind === "completed"
      ? [{ label: progressCompletedLabel(steps()), state: "done" }]
      : steps();
  const restText = () => {
    const standing = progressUpdateStanding(props);
    const steps = restSteps().map((step) => step.label).join(", ");
    return standing ? `${steps}, ${standing} done` : steps;
  };

  return (
    <Show when={count() > 0}>
      <Show
        when={props.kind !== "created"}
        fallback={
          <section
            ref={bindTranscriptEntry}
            class="den-transcript-card progress-strip progress-strip--snapshot"
            data-testid={TEST_IDS[props.kind]}
          >
            <header class="progress-strip__header" {...chromeProps()}>
              <span class="progress-strip__title">{title()}</span>
              <span class="progress-strip__count" data-testid="progress-strip-count">{count()}</span>
            </header>
            <div class="progress-strip__body">
              <StepList steps={steps()} />
            </div>
          </section>
        }
      >
        <TranscriptDisclosureShell
          disclosure={disclosure}
          ref={bindTranscriptEntry}
          class="den-transcript-card progress-strip progress-strip--snapshot progress-strip--update"
          data-testid={TEST_IDS[props.kind]}
        >
          <div class="progress-strip__summary" {...chromeProps()}>
            <TranscriptDisclosureToggle
              disclosure={disclosure}
              inHeader
              class="progress-strip__opener"
              aria-label={`${title()}: ${restText()}`}
            />
            <span class="progress-strip__title">{title()}</span>
            <div class="progress-strip__rest" data-testid="progress-strip-rest" aria-hidden="true">
              <For each={restSteps()}>
                {(step, index) => (
                  <>
                    <Show when={index() > 0}>
                      <span class="progress-strip__rest-sep">·</span>
                    </Show>
                    <span class="progress-strip__rest-step" data-state={step.state}>
                      <span class="progress-strip__glyph">{progressStateGlyph(step.state)}</span>
                      <ProgressStepLabel label={step.label} />
                    </span>
                  </>
                )}
              </For>
            </div>
            <Show when={progressUpdateStanding(props)}>
              <span class="progress-strip__standing" data-testid="progress-strip-standing" aria-hidden="true">
                {progressUpdateStanding(props)}
              </span>
            </Show>
            <span class="den-disclosure-chevron" aria-hidden="true" />
            <span class="progress-strip__count" data-testid="progress-strip-count">{count()}</span>
          </div>
          <TranscriptDisclosurePanel disclosure={disclosure} class="progress-strip__body">
            <StepList steps={steps()} />
          </TranscriptDisclosurePanel>
        </TranscriptDisclosureShell>
      </Show>
    </Show>
  );
}
