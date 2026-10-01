import type { ProjectSummary } from "../project/project-summary.ts";
import { projectThumbnail } from "./thumbnail-store.ts";

/** Identifies a product cover artifact. */
export type ProjectCoverRef = {
  sessionId: string;
  artifactId: string;
};

export function productCoverRef(project: ProjectSummary): ProjectCoverRef | null {
  const sessionId = project.coverRootSessionId?.trim() ?? "";
  const artifactId = project.coverArtifactId?.trim() ?? "";
  if (!sessionId || !artifactId) return null;
  return { sessionId, artifactId };
}

export function resolveProjectCardThumbSrc(
  project: ProjectSummary,
  productCoverObjectUrl: string | null | undefined,
): string | null {
  const cover = productCoverObjectUrl?.trim();
  if (cover) return cover;
  return projectThumbnail(project.id);
}
