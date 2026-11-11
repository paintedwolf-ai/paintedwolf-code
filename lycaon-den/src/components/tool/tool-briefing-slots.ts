import type { JSX } from "solid-js";

/** Places an opened activity card gives its briefing. */
export type ToolBriefingSlots = {
  /** Muted sentence stating why the activity ran. */
  purpose?: JSX.Element;
  /** Replaces the card's deterministic result line. */
  result?: JSX.Element;
  /** Link to the record behind purpose and result. */
  evidence?: JSX.Element;
};
