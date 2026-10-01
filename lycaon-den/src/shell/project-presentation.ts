import type { StageScope } from "./stage-scope.ts";

export type ProjectPresentationFacts = {
  projectId: string | null;
  projectsLoaded: boolean;
  recentsLoaded: boolean;
  contributionsReady: boolean;
  /** Transcript, companion state, and stage content are ready. */
  contentSettled: boolean;
  scope: StageScope;
};

/** Readiness for project chrome, mounting, and reveal. */
type ProjectRegionReadiness = {
  /** Project identity chrome: switcher, folder zone, footer. */
  readonly identity: boolean;
  /** Columns are mounted behind the opening veil. */
  readonly mounted: boolean;
  /** All regions are ready for a shared reveal. */
  readonly open: boolean;
};

export type ProjectPresentation = {
  /** Null on Home and before the destination project ID resolves. */
  readonly projectId: string | null;
  /** Keeps Home covered while the destination project ID resolves. */
  readonly opening: boolean;
  readonly ready: ProjectRegionReadiness;
};

const NO_REGION_READY: ProjectRegionReadiness = {
  identity: false,
  mounted: false,
  open: false,
};

/** Stable identity across Home renders. */
const HOME_PRESENTATION: ProjectPresentation = {
  projectId: null,
  opening: false,
  ready: NO_REGION_READY,
};

/** Stable identity while the destination project ID resolves. */
const OPENING_PRESENTATION: ProjectPresentation = {
  projectId: null,
  opening: true,
  ready: NO_REGION_READY,
};

export function isHomePresentation(presentation: ProjectPresentation): boolean {
  return presentation.projectId === null && !presentation.opening;
}

export function deriveProjectPresentation(
  facts: ProjectPresentationFacts,
): ProjectPresentation {
  const projectId = facts.projectId;
  if (!projectId) {
    return facts.scope.phase === "opening"
      ? OPENING_PRESENTATION
      : HOME_PRESENTATION;
  }

  const scopeSettled =
    facts.scope.project_id === projectId &&
    (facts.scope.phase === "ready" || facts.scope.phase === "project-empty");
  const mounted =
    facts.contributionsReady && facts.recentsLoaded && scopeSettled;

  return {
    projectId,
    opening: false,
    ready: {
      identity: facts.projectsLoaded,
      mounted,
      open: mounted && facts.contentSettled,
    },
  };
}

/** Retains mounted columns during a same-project session switch. */
export function retainMountedProjectPresentation(
  previous: ProjectPresentation | undefined,
  next: ProjectPresentation,
  scope: StageScope,
): ProjectPresentation {
  const sameProject =
    previous != null && previous.projectId === next.projectId;
  const open = next.ready.open;
  const mounted =
    next.ready.mounted ||
    (sameProject &&
      previous.ready.mounted &&
      scope.project_id === next.projectId &&
      scope.phase === "switching");

  // Dependents compare this object by identity.
  if (
    previous &&
    previous.projectId === next.projectId &&
    previous.opening === next.opening &&
    previous.ready.identity === next.ready.identity &&
    previous.ready.mounted === mounted &&
    previous.ready.open === open
  ) {
    return previous;
  }
  if (mounted === next.ready.mounted && open === next.ready.open) return next;
  return {
    projectId: next.projectId,
    opening: next.opening,
    ready: { identity: next.ready.identity, mounted, open },
  };
}
