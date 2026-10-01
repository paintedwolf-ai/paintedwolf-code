import { Show } from "solid-js";
import type { CitationGrounding } from "../../api/types.ts";
import {
  buildCitationGroundingView,
  citationGroundingHadTraceableWork,
} from "../../chat/grounding/citation-grounding-model.ts";
import {
  GROUNDING_BADGE_ARIA_GROUNDED,
  GROUNDING_BADGE_ARIA_PARTIAL,
  GROUNDING_BADGE_TITLE_GROUNDED,
  GROUNDING_BADGE_TITLE_PARTIAL,
} from "../../chat/grounding/grounding-copy.ts";
import { CitationGroundingChicklet } from "./CitationGroundingChicklet.tsx";

type Props = {
  grounding?: CitationGrounding | null;
  /** Opens worker evidence drawer when the chicklet is on a task card. */
  onOpenDetail?: () => void;
};

export function CitationGroundingBadge(props: Props) {
  const view = () => {
    const g = props.grounding;
    return g && citationGroundingHadTraceableWork(g) ? buildCitationGroundingView(g) : undefined;
  };
  const positive = () => view()?.outcome === "traced" || !!view()?.hostAssembled;

  const onClick = (e: MouseEvent) => {
    e.stopPropagation();
    props.onOpenDetail?.();
  };

  return (
    <Show when={view()} keyed>
      {(detail) => (
        <Show
          when={props.onOpenDetail}
          fallback={
            <span
              class="den-citation-grounding-chicklet-inert"
              data-testid="citation-grounding-badge"
            >
              <CitationGroundingChicklet view={detail} />
            </span>
          }
        >
          <button
            type="button"
            class="den-citation-grounding-chicklet-btn"
            data-testid="citation-grounding-badge"
            aria-label={positive() ? GROUNDING_BADGE_ARIA_GROUNDED : GROUNDING_BADGE_ARIA_PARTIAL}
            data-tip={positive() ? GROUNDING_BADGE_TITLE_GROUNDED : GROUNDING_BADGE_TITLE_PARTIAL}
            onClick={onClick}
          >
            <CitationGroundingChicklet view={detail} />
          </button>
        </Show>
      )}
    </Show>
  );
}
