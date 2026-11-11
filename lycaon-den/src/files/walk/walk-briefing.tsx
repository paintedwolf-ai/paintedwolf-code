import { Show, createMemo, type Accessor, type JSX } from "solid-js";
import type { SourceStepBriefingTarget } from "../../api/types.ts";
import { BriefingEvidenceLink, BriefingSummary, snapshotSections } from "../../briefing/BriefingSummary.tsx";
import { requestBriefing, useBriefing, type BriefingTarget } from "../../briefing/briefing-live.ts";
import { fileSummariesEnabled, fileSummariesSettingKnown } from "../../settings/editor/file-summary-settings.ts";

/** What a Walk surface places: one sentence, its evidence link, and retry. */
export type WalkStepBriefing = {
  readonly sentence: JSX.Element | undefined;
  readonly evidence: JSX.Element | undefined;
  /** Asks again; a step whose inputs are unchanged answers without a call. */
  retry: () => void;
};

/**
 * One sentence for a Walk step: what changed when the record says so,
 * otherwise why it was made. Walk never shows both. With no briefing the
 * step renders exactly as it does without one.
 */
export function useWalkStepBriefing(
  projectId: Accessor<string | undefined>,
  stepKey: Accessor<string | undefined>,
  targets: Accessor<ReadonlyMap<string, SourceStepBriefingTarget> | undefined>,
): WalkStepBriefing {
  const target = createMemo<BriefingTarget | null>(() => {
    const key = stepKey();
    const descriptor = key ? targets()?.get(key) : undefined;
    if (!descriptor || !fileSummariesSettingKnown() || !fileSummariesEnabled()) return null;
    return { profileId: "source_step", ref: descriptor as unknown as Record<string, unknown> };
  }, null, { equals: (a, b) => JSON.stringify(a) === JSON.stringify(b) });
  const view = useBriefing(projectId, target);
  const sentence = createMemo(() => {
    const sections = snapshotSections(view().snapshot, view().stalled);
    const change = sections.find((section) => section.id === "change");
    const purpose = sections.find((section) => section.id === "purpose");
    const chosen = change ?? purpose;
    return chosen ? [chosen] : [];
  });
  const recordId = () => view().snapshot?.result?.evidence_ref.record_id ?? null;
  const reference = () => view().snapshot?.result?.evidence_ref;
  const sentenceElement = <BriefingSummary sections={sentence()} recordId={recordId()} data-testid="walk-step-briefing" />;
  const evidenceElement = <BriefingEvidenceLink reference={reference()!} />;
  return {
    get sentence() {
      return sentence().length ? sentenceElement : undefined;
    },
    get evidence() {
      return reference() && sentence().length ? evidenceElement : undefined;
    },
    retry: () => {
      const id = projectId();
      const wanted = target();
      if (id && wanted) requestBriefing(id, wanted, { manual: true });
    },
  };
}

/**
 * A grouped step's lede: the sentence, with the observation caveat moved to
 * a muted note and the evidence link after it; the caveat alone otherwise.
 */
export function WalkPageLede(props: { caveat: string; briefing: WalkStepBriefing }) {
  return (
    <Show when={props.briefing.sentence} fallback={<p class="den-walk-page__lede" data-testid="walk-page-lede">{props.caveat}</p>}>
      {(sentence) => (
        <>
          <div class="den-walk-page__lede" data-testid="walk-page-lede">{sentence()}</div>
          <p class="den-walk-page__note" data-testid="walk-page-caveat">
            {props.caveat}
            <Show when={props.briefing.evidence}>{(link) => <> · {link()}</>}</Show>
          </p>
        </>
      )}
    </Show>
  );
}
