import type { JSX } from "solid-js";

/** Places an opened activity card gives its summary. */
export type ToolBriefingSlots = {
  /** The boxed summary region, placed after what ran. */
  summary?: JSX.Element;
  /** Link to the record behind the summary; present only beside its text. */
  evidence?: JSX.Element;
};
