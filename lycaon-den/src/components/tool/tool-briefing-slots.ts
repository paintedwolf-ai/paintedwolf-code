import type { JSX } from "solid-js";
import type { BriefingEvidenceRef } from "../../api/types.ts";

/** Places an opened activity card gives its summary. */
export type ToolBriefingSlots = {
  /** The boxed summary region, placed after what ran. */
  summary?: JSX.Element;
  /** The record behind the summary; present only beside its text. */
  evidence?: BriefingEvidenceRef;
};
