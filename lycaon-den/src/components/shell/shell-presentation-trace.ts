import { createEffect, untrack } from "solid-js";
import { isPerfCaptureEnabled, recordPerfDiagnostic, type PerfDetail } from "../../chat/stream/den-main-thread-perf.ts";
import { hiddenSplitPanePref, stagePlacementMode } from "../../shell/workspace-layout.ts";
import type { StageScope } from "../../shell/stage-scope.ts";
import type { ResidentStack } from "../../ui/resident-surfaces.ts";

export type ShellPresentationFacts = {
  /** The opening veil covers the workspace. */
  veiled: () => boolean;
  stageStack: ResidentStack;
  chatStack: ResidentStack;
  showStageColumn: () => boolean;
  contextHidden: () => boolean;
  conversationHidden: () => "preview" | "hidden" | null;
  nav: () => string;
  stageScope: () => StageScope;
  presentedSplitLive: () => boolean;
  navUserCollapsed: () => boolean;
  navAutomaticallyCollapsed: () => boolean;
};

/** Neither column shows a published surface and no veil explains the gap. */
export function shellPresentsNothing(facts: ShellPresentationFacts): boolean {
  if (facts.veiled()) return false;
  const stageShown = facts.showStageColumn() && facts.stageStack.displayed() != null && !facts.contextHidden();
  const chatShown = facts.chatStack.displayed() != null && facts.conversationHidden() !== "hidden";
  return !stageShown && !chatShown;
}

function describe(facts: ShellPresentationFacts): PerfDetail {
  const scope = facts.stageScope();
  return {
    nav: facts.nav(),
    scope: scope.phase,
    project: scope.project_id != null,
    stage_column: facts.showStageColumn(),
    stage_displayed: facts.stageStack.displayed() ?? undefined,
    stage_pending: facts.stageStack.pending() ?? undefined,
    chat_displayed: facts.chatStack.displayed() ?? undefined,
    chat_pending: facts.chatStack.pending() ?? undefined,
    placement: stagePlacementMode(),
    split: facts.presentedSplitLive(),
    hidden_pane: hiddenSplitPanePref() ?? undefined,
    nav_collapsed: facts.navUserCollapsed() ? "user" : facts.navAutomaticallyCollapsed() ? "automatic" : undefined,
    visibility: document.visibilityState,
    focused: document.hasFocus(),
  };
}

/** Records each stretch in which the window presents no content. */
export function traceShellPresentation(facts: ShellPresentationFacts): void {
  if (!isPerfCaptureEnabled()) return;
  let emptySince: number | undefined;
  createEffect(() => {
    const empty = shellPresentsNothing(facts);
    if (empty === (emptySince !== undefined)) return;
    if (empty) {
      emptySince = performance.now();
      recordPerfDiagnostic("workspace-empty", untrack(() => describe(facts)));
      return;
    }
    const duration = Math.round(performance.now() - (emptySince ?? 0));
    emptySince = undefined;
    recordPerfDiagnostic("workspace-empty-ended", { ...untrack(() => describe(facts)), duration_ms: duration });
  });
}
