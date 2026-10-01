import type { ModelPolicy, ModelRef, Session } from "../../api/types.ts";
import { shortModelId } from "../../settings/providers/models-editor-model.ts";

export type CoordinatorModelStatus = {
  ref: ModelRef;
  /** True when the session carries its own coordinator override. */
  sessionOverride: boolean;
};

function refSet(ref: ModelRef | undefined): ref is ModelRef {
  return Boolean(ref && ref.provider_id && ref.model);
}

/** Session overrides precede policy defaults; an unassigned model yields null. */
export function coordinatorModelStatus(
  session: Session | undefined,
  effectivePolicy: ModelPolicy | null,
): CoordinatorModelStatus | null {
  if (session?.provider_id && session.model) {
    return {
      ref: { provider_id: session.provider_id, model: session.model },
      sessionOverride: true,
    };
  }
  const policyRef = effectivePolicy?.coordinator;
  if (refSet(policyRef)) {
    return { ref: policyRef, sessionOverride: false };
  }
  return null;
}

/** Chip text — basename only (strip `accounts/…/models/` style paths). */
export function modelChipLabel(status: CoordinatorModelStatus): string {
  return shortModelId(status.ref.model);
}

/** `provider / model` long form for popover rows and tooltips. */
export function modelRefLabel(ref: ModelRef | undefined): string {
  if (!refSet(ref)) return "—";
  return `${ref.provider_id} / ${shortModelId(ref.model)}`;
}
