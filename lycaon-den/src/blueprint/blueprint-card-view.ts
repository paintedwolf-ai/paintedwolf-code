import type { BlueprintMeta } from "../api/types.ts";
import type { BlueprintInlineCardView } from "./blueprint-inline-card-model.ts";

export function blueprintCardViewFromMeta(blueprint: BlueprintMeta): BlueprintInlineCardView {
  return {
    blueprintPath: blueprint.blueprint_path,
    revisionKey: blueprint.revision_key,
    phaseLabel: blueprint.phase_label,
    phase: blueprint.phase,
    status: blueprint.status,
    canApprove: blueprint.can_approve,
    collapsed: blueprint.collapsed,
    showActions: blueprint.show_actions,
  };
}
