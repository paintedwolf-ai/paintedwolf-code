import { Show, createMemo } from "solid-js";
import { BriefingSummary, snapshotSections } from "../../../briefing/BriefingSummary.tsx";
import { briefingCopy } from "../../../briefing/briefing-copy.ts";
import { openBriefingEvidence } from "../../../briefing/briefing-evidence.ts";
import { useBriefing, type BriefingTarget } from "../../../briefing/briefing-live.ts";
import { fileSummariesEnabled, fileSummariesSettingKnown } from "../../../settings/editor/file-summary-settings.ts";

type Props = {
  projectId: string;
  effectId: string;
  line: number;
  /** Closes the popover before Files opens. */
  onOpenEvidence: () => void;
  returnFocus: () => void;
};

/**
 * One sentence for the change that last wrote a line. The target is the
 * change, so every line it wrote, and its Walk step, share one stored result;
 * the request waits until the card stays open.
 */
export function LineFactsBriefing(props: Props) {
  const target = createMemo<BriefingTarget | null>(() => {
    if (!fileSummariesSettingKnown() || !fileSummariesEnabled()) return null;
    return { profileId: "source_step", ref: { kind: "effect", target_key: props.effectId, scope_fingerprint: "", scope: { kind: "file", root_ids: [] } } };
  }, null, { equals: (a, b) => JSON.stringify(a) === JSON.stringify(b) });
  const view = useBriefing(() => props.projectId, target);
  const sentence = createMemo(() => {
    const sections = snapshotSections(view().snapshot, view().stalled);
    const chosen = sections.find((section) => section.id === "change") ?? sections.find((section) => section.id === "purpose");
    return chosen ? [chosen] : [];
  });
  const reference = () => view().snapshot?.result?.evidence_ref;
  return (
    <Show when={sentence().length}>
      <div class="files-line-facts__briefing" data-testid="line-facts-briefing">
        <BriefingSummary sections={sentence()} recordId={reference()?.record_id ?? null} />
        <Show when={reference()}>
          {(ref) => (
            <button type="button" class="den-briefing-evidence-link" aria-label={briefingCopy.evidenceLinkName} data-testid="line-facts-evidence"
              onClick={() => {
                props.onOpenEvidence();
                openBriefingEvidence(ref(), { label: `line ${props.line}`, focus: props.returnFocus });
              }}>
              {briefingCopy.evidenceLink}
            </button>
          )}
        </Show>
      </div>
    </Show>
  );
}
