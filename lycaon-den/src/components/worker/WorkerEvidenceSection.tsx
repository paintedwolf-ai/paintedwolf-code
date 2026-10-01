import { For, Show } from "solid-js";
import type { WorkerSummaryMeta, WorkerTask } from "../../api/types.ts";
import {
  buildCitationGroundingView,
  citationGroundingPresent,
} from "../../chat/grounding/citation-grounding-model.ts";
import {
  buildWorkerEvidenceFallbackView,
  resolveCitationGrounding,
  type WorkerEvidenceOutcome,
  workerEvidenceJobId,
  workerEvidenceSectionId,
} from "../../chat/worker/worker-evidence-model.ts";
import { workerRowStatus } from "../../chat/worker/workers-model.ts";
import { openInSearch } from "../../search/search-nav.ts";
import {
  CitationGroundingPanel,
  type CitationExploreContext,
} from "../citation/CitationGroundingPanel.tsx";
import {
  WorkerTranscriptSection,
  type WorkerSectionStatus,
} from "./WorkerTranscriptSection.tsx";

/** Pending evidence pulses only while the worker runs. */
function evidenceSectionStatus(
  outcome: WorkerEvidenceOutcome,
  worker: WorkerTask,
): WorkerSectionStatus {
  switch (outcome) {
    case "traced":
      return "complete";
    case "partial":
      return "partial";
    case "failed":
      return "failed";
    case "pending":
      return workerRowStatus(worker) === "running" ? "working" : "pending";
  }
}

type Props = {
  worker: WorkerTask;
  summaryMeta?: WorkerSummaryMeta;
  highlight?: boolean;
  onSelect: () => void;
};

export function WorkerEvidenceSection(props: Props) {
  const jobId = () =>
    workerEvidenceJobId(props.summaryMeta, props.worker, props.worker.id);
  const projectId = () => props.worker.project_id?.trim() || "";
  const exploreContext = (): CitationExploreContext => ({
    sessionId: props.worker.child_session_id?.trim() || undefined,
    legId: props.worker.leg_id?.trim() || undefined,
  });
  const onExplore = () => {
    const origin = projectId();
    if (!origin) return undefined;
    return (query: string) => openInSearch(origin, query);
  };
  const grounding = () =>
    resolveCitationGrounding(props.summaryMeta, props.worker);
  const groundingView = () => {
    const g = grounding();
    return g && citationGroundingPresent(g)
      ? buildCitationGroundingView(g)
      : undefined;
  };
  const fallback = () =>
    buildWorkerEvidenceFallbackView(
      props.summaryMeta,
      props.worker,
      props.worker.id,
    );

  return (
    <>
      <Show when={groundingView()} keyed>
        {(view) => (
          <WorkerTranscriptSection
            title="Evidence"
            sectionKey="evidence"
            id={workerEvidenceSectionId(jobId() ?? props.worker.id)}
            class="den-worker-transcript-evidence"
            classList={{
              "den-worker-transcript-evidence--traced":
                view.outcome === "traced" && view.hadTraceableWork,
              "den-worker-transcript-evidence--partial":
                view.outcome === "partial" && view.hadTraceableWork,
              "den-worker-transcript-evidence--highlight": props.highlight,
            }}
            data-testid="worker-evidence-section"
            data-evidence-outcome={view.outcome}
            status={evidenceSectionStatus(view.outcome, props.worker)}
            onSelect={() => props.onSelect()}
          >
            <CitationGroundingPanel
              view={view}
              projectId={projectId() || undefined}
              exploreContext={exploreContext()}
              onExplore={onExplore()}
            />
          </WorkerTranscriptSection>
        )}
      </Show>
      <Show when={fallback()} keyed>
        {(detail) => (
          <WorkerTranscriptSection
            title="Evidence"
            sectionKey="evidence"
            id={workerEvidenceSectionId(detail.jobId)}
            class="den-worker-transcript-evidence"
            classList={{
              "den-worker-transcript-evidence--traced":
                detail.outcome === "traced",
              "den-worker-transcript-evidence--partial":
                detail.outcome === "partial",
              "den-worker-transcript-evidence--failed":
                detail.outcome === "failed",
              "den-worker-transcript-evidence--highlight": props.highlight,
            }}
            data-testid="worker-evidence-section"
            data-evidence-outcome={detail.outcome}
            status={evidenceSectionStatus(detail.outcome, props.worker)}
            onSelect={() => props.onSelect()}
          >
            <p class="den-worker-transcript-evidence-headline">
              {detail.headline}
            </p>
            <Show when={detail.statusLines.length > 0}>
              <ul class="den-worker-transcript-evidence-lines">
                <For each={detail.statusLines}>{(line) => <li>{line}</li>}</For>
              </ul>
            </Show>
          </WorkerTranscriptSection>
        )}
      </Show>
    </>
  );
}
