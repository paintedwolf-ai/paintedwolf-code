import type { CitationGroundingView } from "../../chat/grounding/citation-grounding-model.ts";
import { Show } from "solid-js";
import {
  GROUNDING_PROVENANCE_MARK,
  GROUNDING_STATUS_GROUNDED,
  GROUNDING_STATUS_PARTIAL,
} from "../../chat/grounding/grounding-copy.ts";
import { cn } from "../../shared/cn.ts";

type Props = {
  view: CitationGroundingView;
  class?: string;
};

export function CitationGroundingChicklet(props: Props) {
  const positive = () =>
    props.view.outcome === "traced" || props.view.hostAssembled;
  const label = () =>
    positive() ? GROUNDING_STATUS_GROUNDED : GROUNDING_STATUS_PARTIAL;
  return (
    <span
      class={cn("den-citation-grounding-chicklet", props.class)}
      classList={{
        "den-citation-grounding-chicklet--traced": positive(),
        "den-citation-grounding-chicklet--partial": !positive(),
      }}
      data-outcome={props.view.outcome}
      data-host-assembled={props.view.hostAssembled ? "true" : undefined}
    >
      <span class="den-citation-grounding-chicklet-mark" aria-hidden="true">
        <Show when={positive()} fallback="!">
          {GROUNDING_PROVENANCE_MARK}
        </Show>
      </span>
      <span class="den-citation-grounding-chicklet-label">{label()}</span>
    </span>
  );
}
