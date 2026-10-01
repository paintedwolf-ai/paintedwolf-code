import type {
  ProjectTrust,
  ProjectTrustSurface,
} from "../../api/types.ts";

export function presentSurfaces(trust: Pick<ProjectTrust, "surfaces">): ProjectTrustSurface[] {
  return trust.surfaces.filter((surface) => surface.count > 0);
}

export function steeringSurfaces(trust: Pick<ProjectTrust, "surfaces">): ProjectTrustSurface[] {
  return presentSurfaces(trust).filter((s) => s.group === "steering");
}

export function replacesDefaultSurfaces(
  trust: Pick<ProjectTrust, "surfaces">,
): ProjectTrustSurface[] {
  return presentSurfaces(trust).filter((s) => s.group === "replaces_defaults");
}

export function suggestionSurfaces(
  trust: Pick<ProjectTrust, "surfaces">,
): ProjectTrustSurface[] {
  return presentSurfaces(trust).filter((s) => s.group === "suggestion");
}

export type TrustDotState = "seen" | "unseen" | "empty";

export function trustDotState(trust: ProjectTrust): TrustDotState {
  if (trust.unread_count > 0) return "unseen";
  if (trust.review.changes.length === 0) return "empty";
  return "seen";
}

export function instructionLineCount(surface: ProjectTrustSurface): number {
  return surface.items.reduce((sum, item) => sum + (item.lines ?? 0), 0);
}
