import { Show } from "solid-js";
import type { CitationGrounding } from "../../api/types.ts";
import {
  buildCitationGroundingView,
  citationGroundingHadTraceableWork,
} from "../../chat/grounding/citation-grounding-model.ts";
import {
  groundingEvidenceEntryKey,
  useTranscriptEntry,
} from "../../chat/transcript/presentation/transcript-entry.ts";
import {
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  useTranscriptDisclosure,
} from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { CitationEvidenceSection } from "./CitationEvidenceSection.tsx";
import { CitationGroundingChicklet } from "./CitationGroundingChicklet.tsx";
import type { CitationExploreContext } from "./CitationGroundingPanel.tsx";

import { TranscriptChickletSummary } from "../transcript/TranscriptChickletSummary.tsx";

type Props = {
  grounding?: CitationGrounding | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  exploreContext?: CitationExploreContext;
  onExplore?: (query: string) => void;
  sessionId?: string | null;
  /** The assistant row the evidence belongs to. */
  rowKey?: string;
};

export function CitationEvidenceChicklet(props: Props) {
  const view = () => {
    const g = props.grounding;
    return g && citationGroundingHadTraceableWork(g)
      ? buildCitationGroundingView(g)
      : undefined;
  };
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.rowKey
      ? { sessionId: props.sessionId ?? undefined, entryKey: groundingEvidenceEntryKey(props.rowKey) }
      : undefined,
  );
  const disclosure = useTranscriptDisclosure(() =>
    props.rowKey ? transcriptDisclosureKey.groundingEvidence(props.rowKey) : undefined,
  );

  return (
    <Show when={view()} keyed>
      {(detail) => (
        <TranscriptDisclosureShell
          disclosure={disclosure}
          ref={bindTranscriptEntry}
          class="den-citation-evidence-chicklet den-transcript-disclosure-card"
          data-testid="citation-evidence-chicklet"
          data-evidence-outcome={detail.outcome}
        >
          <TranscriptChickletSummary disclosure={disclosure}>
            <CitationGroundingChicklet view={detail} />
          </TranscriptChickletSummary>
          <TranscriptDisclosurePanel disclosure={disclosure} class="den-citation-evidence-chicklet-body">
            <CitationEvidenceSection
              view={detail}
              sectionClass="den-worker-transcript-evidence"
              sectionClassList={{
                "den-worker-transcript-evidence--traced":
                  detail.outcome === "traced" && detail.hadTraceableWork,
                "den-worker-transcript-evidence--partial":
                  detail.outcome === "partial" && detail.hadTraceableWork,
              }}
              testId="citation-evidence-chicklet-panel"
              projectId={props.projectId}
              rootRefs={props.rootRefs}
              exploreContext={props.exploreContext}
              onExplore={props.onExplore}
            />
          </TranscriptDisclosurePanel>
        </TranscriptDisclosureShell>
      )}
    </Show>
  );
}
