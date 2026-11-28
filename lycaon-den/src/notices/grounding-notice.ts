import type { GroundingEvent } from "../api/types.ts";
import { CLIENT_NOTICES } from "./client-notices.generated.ts";
import { publishNotice } from "./notice-store.ts";

/** Host grounding event code emitted when a cited stored evidence body cannot be verified. */
export const EVIDENCE_BODY_UNAVAILABLE_CODE = "EVIDENCE_BODY_UNAVAILABLE";

/**
 * Publishes an actionable session notice when the host reports a grounding
 * condition that requires re-observing a source (such as an unavailable stored
 * evidence body).
 */
export function publishGroundingEventNotice(
  event: GroundingEvent,
  projectId: string,
): void {
  const code = event.code?.trim();
  const sessionId = event.session_id?.trim();
  const pid = projectId.trim();
  if (code !== EVIDENCE_BODY_UNAVAILABLE_CODE || !sessionId || !pid) {
    return;
  }
  const copy = CLIENT_NOTICES.evidence_body_unavailable;
  publishNotice(
    {
      severity: "warning",
      code: EVIDENCE_BODY_UNAVAILABLE_CODE,
      title: copy.title,
      message: copy.message,
      suggestedAction: copy.suggestedAction,
      source: "host_error",
    },
    { kind: "session", projectId: pid, sessionId },
  );
}
