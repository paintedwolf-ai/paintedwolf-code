import { buildCanonicalArtifactPlacement } from "./visual-artifact-canonical.ts";
import { clearVisualArtifactSrcs } from "./visual-artifact-src.ts";
import { clearFrameArchives } from "./frame-archive-cache.ts";
import { clearArtifactDeletionMemory } from "./artifact-change-store.ts";

/** Remounted artifacts skip the intro after their first paint in a session. */
const introDoneIds = new Set<string>();

export function visualArtifactIntroDone(artifactId: string): boolean {
  const id = artifactId.trim();
  return id.length > 0 && introDoneIds.has(id);
}

export function markVisualArtifactIntroDone(artifactId: string): void {
  const id = artifactId.trim();
  if (id) introDoneIds.add(id);
}

/** Hydrated thumbnails skip the arrival animation. */
export function noteVisualArtifactIntroBaseline(
  messages: readonly import("../../api/types.ts").Message[],
): void {
  for (const placement of buildCanonicalArtifactPlacement(messages).values()) {
    markVisualArtifactIntroDone(placement.artifactId);
  }
}

/** Clear all visual presentation state retained for the active session. */
export function clearVisualArtifactSessionMemory(): void {
  introDoneIds.clear();
  clearVisualArtifactSrcs();
  clearFrameArchives();
}

/** Test-only — clears session visual presentation state between cases. */
export function resetVisualArtifactSessionMemoryForTests(): void {
  clearVisualArtifactSessionMemory();
  clearArtifactDeletionMemory();
}
