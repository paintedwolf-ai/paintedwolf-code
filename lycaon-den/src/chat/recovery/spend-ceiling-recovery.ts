import type { AppStore } from "../../store/app-state-model.ts";
import { isChatActivityLive } from "../session/session-activity.ts";
import { hasComposerDraft } from "../composer/composer-drafts.ts";
import { pendingAttachmentsForSession } from "../composer/composer-attachment-store.ts";

/** Unsent content belongs to the composer, including attachment-only requests. */
export function hasSpendRecoveryDraft(sessionId: string): boolean {
  return hasComposerDraft(sessionId) || pendingAttachmentsForSession(sessionId).length > 0;
}

/** Continue the existing context without replaying a workflow-launch command. */
export async function resumeAfterSpendCeiling(
  appStore: AppStore,
  sessionId: string,
  send: (text: string) => Promise<unknown>,
): Promise<boolean> {
  if (appStore.state.currentSession?.id !== sessionId || isChatActivityLive(appStore, sessionId)) return false;
  if (hasSpendRecoveryDraft(sessionId)) return true;
  return await send("The spending ceiling has been raised. Continue the current work from where it stopped, preserving completed work.") !== false;
}
