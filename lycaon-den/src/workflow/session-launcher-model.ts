import type { WorkflowSummary } from "../api/types.ts";

/** A featured workflow rendered as a quick-start tile under the composer on a fresh session. */
export type LauncherTile = {
  workflow: WorkflowSummary;
  title: string;
  description?: string;
  /** Icon slot name; an unknown one falls back to the generic workflow mark. */
  icon: string;
  /** Repo-oriented workflow with no repo attached — visible but not startable. */
  disabled: boolean;
  disabledReason?: string;
};

const NEEDS_REPO_REASON = "Attach a repo to enable.";

export function launcherGlyph(workflow: WorkflowSummary): string {
  const explicit = workflow.icon?.trim();
  if (explicit) return explicit;
  return "workflow";
}

function tilesForWorkflows(
  workflows: readonly WorkflowSummary[],
  opts: { hasRepo: boolean },
): LauncherTile[] {
  return workflows.map((wf) => {
    const disabled = wf.requires_repo === true && !opts.hasRepo;
    return {
      workflow: wf,
      title: wf.name ?? wf.id,
      description: wf.description?.trim() || undefined,
      icon: launcherGlyph(wf),
      disabled,
      disabledReason: disabled ? NEEDS_REPO_REASON : undefined,
    };
  });
}

function sortWorkflowsByName(
  workflows: readonly WorkflowSummary[],
): WorkflowSummary[] {
  return [...workflows].sort((a, b) =>
    (a.name ?? a.id).toLowerCase().localeCompare((b.name ?? b.id).toLowerCase()),
  );
}

/** Featured catalog entries sorted by name. */
export function featuredWorkflows(
  catalog: readonly WorkflowSummary[],
): WorkflowSummary[] {
  return sortWorkflowsByName(catalog.filter((wf) => wf.featured === true));
}

/** Catalog entries whose manifest declares a blueprint block, sorted by name. */
export function blueprintSupportingWorkflows(
  catalog: readonly WorkflowSummary[],
): WorkflowSummary[] {
  return sortWorkflowsByName(
    catalog.filter((wf) => wf.supports_blueprints === true),
  );
}

/** Primary tile budget for the Blueprints-zone launcher; the rest live behind More. */
export const BLUEPRINT_LAUNCHER_PRIMARY_CAP = 4;

export type BlueprintLauncherPresentation = {
  /** Primary tiles (≤ {@link BLUEPRINT_LAUNCHER_PRIMARY_CAP}). */
  tiles: LauncherTile[];
  /** Blueprint-supporting workflows that did not fit the primary strip. */
  more: LauncherTile[];
};

/** Featured blueprint-supporting workflows fill the strip first, then by name; overflow goes to More. */
export function blueprintLauncherPresentation(
  catalog: readonly WorkflowSummary[],
  opts: { hasRepo: boolean },
): BlueprintLauncherPresentation {
  const all = blueprintSupportingWorkflows(catalog);
  const ordered = [...all].sort((a, b) => {
    const af = a.featured === true ? 0 : 1;
    const bf = b.featured === true ? 0 : 1;
    if (af !== bf) return af - bf;
    return (a.name ?? a.id)
      .toLowerCase()
      .localeCompare((b.name ?? b.id).toLowerCase());
  });
  const primary = ordered.slice(0, BLUEPRINT_LAUNCHER_PRIMARY_CAP);
  const overflow = ordered.slice(BLUEPRINT_LAUNCHER_PRIMARY_CAP);
  return {
    tiles: tilesForWorkflows(primary, opts),
    more: tilesForWorkflows(overflow, opts),
  };
}

/** Build launcher tiles, disabling repo-oriented workflows when no repo is attached. */
export function sessionLauncherTiles(
  catalog: readonly WorkflowSummary[],
  opts: { hasRepo: boolean },
): LauncherTile[] {
  return tilesForWorkflows(featuredWorkflows(catalog), opts);
}

/** Shows the launcher only for a fresh idle chat without a submitted Home prompt. */
export function shouldShowSessionLauncher(opts: {
  hasInitialPrompt: boolean;
  hasMessages: boolean;
  hasCatalogRun: boolean;
  streaming: boolean;
  tileCount: number;
}): boolean {
  if (opts.tileCount === 0) return false;
  return (
    !opts.hasInitialPrompt &&
    !opts.hasMessages &&
    !opts.hasCatalogRun &&
    !opts.streaming
  );
}
