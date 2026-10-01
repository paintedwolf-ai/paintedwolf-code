import { Show } from "solid-js";
import type { CitationGrounding } from "../../api/types.ts";
import {
  buildCitationGroundingView,
  citationGroundingPresent,
  type CitationGroundingView,
} from "../../chat/grounding/citation-grounding-model.ts";
import { CitationGroundingPanel, type CitationExploreContext } from "./CitationGroundingPanel.tsx";

type Props = {
  grounding?: CitationGrounding | null;
  /** Pre-built view when the caller already resolved grounding. */
  view?: CitationGroundingView;
  sectionClass: string;
  sectionClassList?: Record<string, boolean | undefined>;
  testId: string;
  id?: string;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  exploreContext?: CitationExploreContext;
  onExplore?: (query: string) => void;
};

export function CitationEvidenceSection(props: Props) {
  const view = () => {
    if (props.view) return props.view;
    const g = props.grounding;
    return g && citationGroundingPresent(g)
      ? buildCitationGroundingView(g)
      : undefined;
  };

  return (
    <Show when={view()} keyed>
      {(detail) => (
        <section
          id={props.id}
          class={props.sectionClass}
          classList={props.sectionClassList}
          data-testid={props.testId}
          data-evidence-outcome={detail.outcome}
        >
          <h3 class="transcript-pane__subhead">Evidence</h3>
          <CitationGroundingPanel
            view={detail}
            projectId={props.projectId}
            rootRefs={props.rootRefs}
            exploreContext={props.exploreContext}
            onExplore={props.onExplore}
          />
        </section>
      )}
    </Show>
  );
}
