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

type Props = {
  worker?: Accessor<WorkerTask>;
  resultHandle?: Accessor<string | undefined>;
  projectId?: Accessor<string | undefined>;
  inline?: boolean;
  onSelect?: () => void;
  onPresence?: (shown: boolean) => void;
};

/**
 * Summary of a settled worker round, keyed by its result handle. It requests a
 * briefing only while the worker's detail or expanded round is open.
 */
export function WorkerSummarySection(props: Props) {
  const handle = () =>
    (props.resultHandle ? props.resultHandle()?.trim() : props.worker?.().result_handle?.trim()) ||
    "";
  const project = () =>
    (props.projectId ? props.projectId()?.trim() : props.worker?.().project_id?.trim()) ||
    undefined;
  const target = createMemo<BriefingTarget | null>(
    () => {
      const resultHandle = handle();
      if (!resultHandle || !fileSummariesSettingKnown() || !fileSummariesEnabled()) return null;
      return { profileId: "worker_result", ref: { result_handle: resultHandle } };
    },
    null,
    { equals: (a, b) => JSON.stringify(a) === JSON.stringify(b) },
  );
  const view = useBriefing(project, target);
  const sections = createMemo<SummarySection[]>(() => {
    const snapshot = view().snapshot;
    const result = snapshot?.result?.summary[0];
    if (result) {
      const blocks = workerSummaryBlocks(result.text);
      return LABELS.filter((label) => blocks.get(label)).map((label) => ({
        id: label,
        label,
        text: blocks.get(label),
        source: result.source,
      }));
    }
    if (snapshot?.status === "pending" && !view().stalled) {
      return LABELS.map((label) => ({ id: label, label }));
    }
    return [];
  });
  const reference = () => view().snapshot?.result?.evidence_ref;
  createEffect(() => props.onPresence?.(sections().length > 0));
  const body = () => (
    <>
      <BriefingSummary sections={sections()} recordId={reference()?.record_id ?? null} />
      <Show when={reference()} keyed>
        {(ref) => <BriefingEvidenceLink reference={ref} />}
      </Show>
    </>
  );
  return (
    <Show when={sections().length}>
      <Show
        when={props.inline}
        fallback={
          <WorkerTranscriptSection
            title="Summary"
            sectionKey="summary"
            status="complete"
            data-testid="worker-summary"
            onSelect={() => props.onSelect?.()}
          >
            {body()}
          </WorkerTranscriptSection>
        }
      >
        <div
          class="den-worker-round-summary"
          data-testid="worker-round-summary"
          data-result-handle={handle()}
        >
          {body()}
        </div>
      </Show>
    </Show>
  );
}
