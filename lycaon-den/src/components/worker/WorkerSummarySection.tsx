import { Show, createEffect, createMemo, type Accessor } from "solid-js";
import type { WorkerTask } from "../../api/types.ts";
import { BriefingEvidenceLink, BriefingSummary, type SummarySection } from "../../briefing/BriefingSummary.tsx";
import { useBriefing, type BriefingTarget } from "../../briefing/briefing-live.ts";
import { fileSummariesEnabled, fileSummariesSettingKnown } from "../../settings/editor/file-summary-settings.ts";
import { WorkerTranscriptSection } from "./WorkerTranscriptSection.tsx";

const LABELS = ["Contribution", "Verification", "Remaining"] as const;

/** Splits the worker summary into its labelled blocks, in display order. */
export function workerSummaryBlocks(text: string): Map<string, string> {
  const blocks = new Map<string, string>();
  for (const line of text.split("\n")) {
    const label = LABELS.find((candidate) => line.startsWith(`${candidate}:`));
    if (label) blocks.set(label, line.slice(label.length + 1).trim());
  }
  return blocks;
}

/**
 * Summary of a finished worker, before its Task. It asks only while the
 * worker's detail is open; the roster and completion alone never ask.
 */
export function WorkerSummarySection(props: { worker: Accessor<WorkerTask>; onSelect: () => void; onPresence: (shown: boolean) => void }) {
  const target = createMemo<BriefingTarget | null>(() => {
    const worker = props.worker();
    if (!worker.completed_at || !fileSummariesSettingKnown() || !fileSummariesEnabled()) return null;
    return { profileId: "worker_result", ref: { job_id: worker.id, result_revision: worker.completed_at } };
  }, null, { equals: (a, b) => JSON.stringify(a) === JSON.stringify(b) });
  const view = useBriefing(() => props.worker().project_id?.trim() || undefined, target);
  const sections = createMemo<SummarySection[]>(() => {
    const snapshot = view().snapshot;
    const result = snapshot?.result?.summary[0];
    if (result) {
      const blocks = workerSummaryBlocks(result.text);
      return LABELS.filter((label) => blocks.get(label)).map((label) => ({ id: label, label, text: blocks.get(label), source: result.source }));
    }
    if (snapshot?.status === "pending" && !view().stalled) return LABELS.map((label) => ({ id: label, label }));
    return [];
  });
  const reference = () => view().snapshot?.result?.evidence_ref;
  createEffect(() => props.onPresence(sections().length > 0));
  return (
    <Show when={sections().length}>
      <WorkerTranscriptSection title="Summary" sectionKey="summary" status="complete" data-testid="worker-summary" onSelect={props.onSelect}>
        <BriefingSummary sections={sections()} recordId={reference()?.record_id ?? null} />
        <Show when={reference()}>{(ref) => <BriefingEvidenceLink reference={ref()} />}</Show>
      </WorkerTranscriptSection>
    </Show>
  );
}
