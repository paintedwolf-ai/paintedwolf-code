import { createMemo } from "solid-js";
import type { StageColumnResolution } from "./stage-placement.ts";

type StageLayout = {
  projectId: string | null;
  column: StageColumnResolution;
};

/** Keeps outgoing split geometry until its replacement surface publishes. */
export function useStageLayoutPresentation(facts: {
  projectId: () => string | null;
  mounted: () => boolean;
  column: () => StageColumnResolution;
  requestedSurface: () => string | null;
  displayedSurface: () => string | null;
}): () => StageColumnResolution {
  const layout = createMemo((previous: StageLayout | undefined): StageLayout => {
    const projectId = facts.projectId();
    const column = facts.column();
    const requested = facts.requestedSurface();
    if (!facts.mounted()) {
      return { projectId, column: { splitLive: false, stageId: null } };
    }
    if (
      previous?.projectId === projectId &&
      previous.column.splitLive &&
      !column.splitLive &&
      requested !== null &&
      facts.displayedSurface() !== requested
    ) {
      return previous;
    }
    return { projectId, column };
  });
  return () => layout().column;
}
